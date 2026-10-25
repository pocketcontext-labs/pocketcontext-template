package tunnel

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/derp/derpserver"
	"tailscale.com/envknob"
	"tailscale.com/net/netcheck"
	"tailscale.com/net/stun/stuntest"
	"tailscale.com/syncs"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/nettype"
)

// TestMain keeps the engines off the real network, as tailcat's own tests do:
// no gateway port-mapping probes and no captive portal checks.
func TestMain(m *testing.M) {
	envknob.Setenv("IN_TS_TEST", "true")
	netcheck.HookStartCaptivePortalDetection.SetForTest(func(context.Context, *netcheck.Client, *tailcfg.DERPMap, tailcfg.DERPRegionID, func(bool)) (<-chan struct{}, func()) {
		return syncs.ClosedChan(), func() {}
	})
	os.Exit(m.Run())
}

func TestIdentityPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "tunnel.json")
	first, err := LoadIdentity(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("identity file mode = %v, %v; want 0600", info.Mode(), err)
		}
	}
	again, err := LoadIdentity(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Key.Equal(first.Key) || !again.PresharedKey.Equal(first.PresharedKey) {
		t.Fatal("a second load changed the identity")
	}
	rotated, err := LoadIdentity(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Key.Equal(first.Key) || rotated.PresharedKey.Equal(first.PresharedKey) {
		t.Fatal("rotation kept the old keys")
	}
	saved, err := LoadIdentity(path, false)
	if err != nil || !saved.Key.Equal(rotated.Key) {
		t.Fatalf("the rotated identity was not saved: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"key":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(path, false); err == nil || !strings.Contains(err.Error(), "--new-address") {
		t.Fatalf("damaged identity: %v", err)
	}
}

func TestParseAddressDoesNotEcho(t *testing.T) {
	_, err := ParseAddress("tcNOT-A-REAL-ADDRESS-secret")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("ParseAddress = %v", err)
	}
	valid := tailcat.NewPrivateKey().Public
	valid.RegionID = 1
	if _, err := ParseAddress(string(valid.Addr())); err != nil {
		t.Fatalf("valid address refused: %v", err)
	}
}

func TestListenRefusesBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	if ln, err := Listen("127.0.0.1", port); err == nil {
		ln.Close()
		t.Fatal("listened on a busy port")
	} else if !strings.Contains(err.Error(), "exact port") {
		t.Fatalf("busy port error = %v", err)
	}
}

// localRelay runs a DERP relay and STUN server on 127.0.0.1 for one test.
func localRelay(t *testing.T) *tailcfg.DERPRegion {
	t.Helper()
	d := derpserver.New(key.NewNode(), func(string, ...any) {})
	relay := httptest.NewUnstartedServer(derpserver.Handler(d))
	relay.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	relay.StartTLS()
	stunAddr, stopSTUN := stuntest.ServeWithPacketListener(t, nettype.Std{})
	t.Cleanup(func() {
		relay.CloseClientConnections()
		relay.Close()
		d.Close()
		stopSTUN()
	})
	return &tailcfg.DERPRegion{RegionID: 1, RegionCode: "test", Nodes: []*tailcfg.DERPNode{{
		Name: "t1", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none",
		STUNPort: stunAddr.Port, DERPPort: relay.Listener.Addr().(*net.TCPAddr).Port,
		InsecureForTests: true, STUNTestIP: "127.0.0.1",
	}}}
}

// TestServeForward carries HTTP through a tunnel over an in-process DERP relay,
// without the public network.
func TestServeForward(t *testing.T) {
	region := localRelay(t)

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "through the tunnel") })}
	go web.Serve(backend)
	t.Cleanup(func() { web.Close() })
	port := backend.Addr().(*net.TCPAddr).Port

	id, err := LoadIdentity(filepath.Join(t.TempDir(), "tunnel.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	id.Region = region
	serve := func() (tailcat.Addr, context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan tailcat.Addr, 1), make(chan error, 1)
		go func() { done <- Serve(ctx, port, id, func(a tailcat.Addr) { ready <- a }) }()
		select {
		case addr := <-ready:
			return addr, cancel, done
		case err := <-done:
			t.Fatalf("Serve: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatal("Serve did not become ready")
		}
		return "", cancel, done
	}
	addr, stopServe, served := serve()

	// The server's port is busy here, so the forward listens on another one;
	// it still dials the server's port through the tunnel.
	ln, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stopForward := context.WithCancel(context.Background())
	forwarded := make(chan error, 1)
	go func() { forwarded <- Forward(ctx, addr, ln, port) }()

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "through the tunnel" {
		t.Fatalf("body = %q", body)
	}

	// No other port of the server is reachable.
	other := &tailcat.Client{Server: addr, Logf: quiet}
	defer other.Close()
	dial, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if conn, err := other.DialTCPPort(dial, uint16(port+1)); err == nil {
		conn.Close()
		t.Fatal("reached a port that is not served")
	}
	cancel()

	stopForward()
	if err := <-forwarded; err != nil {
		t.Fatalf("Forward: %v", err)
	}
	stopServe()
	if err := <-served; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// The same identity gives the same address after a restart.
	again, stopAgain, servedAgain := serve()
	stopAgain()
	<-servedAgain
	if again != addr {
		t.Fatal("the address changed across a restart with the same identity")
	}
}

// TestForwardSurvivesServeRestart keeps one forward running while the server
// side restarts with the same identity: the first connection after the
// restart must get through, promptly.
func TestForwardSurvivesServeRestart(t *testing.T) {
	region := localRelay(t)

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}
	go web.Serve(backend)
	t.Cleanup(func() { web.Close() })
	port := backend.Addr().(*net.TCPAddr).Port

	id, err := LoadIdentity(filepath.Join(t.TempDir(), "tunnel.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	id.Region = region
	serve := func() (tailcat.Addr, context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan tailcat.Addr, 1), make(chan error, 1)
		go func() { done <- Serve(ctx, port, id, func(a tailcat.Addr) { ready <- a }) }()
		select {
		case addr := <-ready:
			return addr, cancel, done
		case err := <-done:
			t.Fatalf("Serve: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatal("Serve did not become ready")
		}
		return "", cancel, done
	}
	addr, stopServe, served := serve()

	ln, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stopForward := context.WithCancel(context.Background())
	forwarded := make(chan error, 1)
	go func() { forwarded <- Forward(ctx, addr, ln, port) }()
	defer func() {
		stopForward()
		<-forwarded
	}()

	// Each request opens a new connection, as each CLI command does: a
	// connection opened before the restart is lost with the old session.
	get := func() error {
		client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	if err := get(); err != nil {
		t.Fatal(err)
	}

	stopServe()
	<-served
	_, stopAgain, servedAgain := serve()
	defer func() {
		stopAgain()
		<-servedAgain
	}()

	start := time.Now()
	if err := get(); err != nil {
		t.Fatalf("the forward did not recover after the server restarted: %v", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("the forward took %v to recover", took)
	}
}

// TestBuildTagsFollowTailcat keeps build-tags.txt in step with the tailcat
// release in go.mod: it must hold every ts_omit_ tag tailcat ships with.
func TestBuildTagsFollowTailcat(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/tailscale/tailcat").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	upstream, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "build-tags.txt"))
	if err != nil {
		t.Skipf("tailcat module source unavailable: %v", err)
	}
	ours, err := os.ReadFile(filepath.Join("..", "..", "build-tags.txt"))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tag := range strings.Split(strings.TrimSpace(string(ours)), ",") {
		have[tag] = true
	}
	for _, tag := range strings.Split(strings.TrimSpace(string(upstream)), ",") {
		if !have[tag] {
			t.Errorf("build-tags.txt lacks tailcat's %s", tag)
		}
	}
}

// TestReplacedClientDrains keeps a replaced client open while it still carries
// a connection, rebuilds it once for concurrent failures, and closes it when
// its last connection ends.
func TestReplacedClientDrains(t *testing.T) {
	valid := tailcat.NewPrivateKey().Public
	valid.RegionID = 1
	f := newForwarder(valid.Addr())
	defer f.close()

	open := f.acquire() // a healthy proxied connection
	failed := f.acquire()
	f.replace(failed)
	f.replace(failed) // a second failure of the same client
	f.release(failed)
	if f.cur == open || len(f.all) != 2 {
		t.Fatalf("after one replacement: %d clients, want 2", len(f.all))
	}
	if !f.all[open] {
		t.Fatal("the replaced client was closed while it still carried a connection")
	}
	f.release(open)
	if f.all[open] || len(f.all) != 1 {
		t.Fatal("the replaced client was not closed after its last connection ended")
	}
	if next := f.acquire(); next != f.cur || next.retired {
		t.Fatal("a new connection did not use the new client")
	} else {
		f.release(next)
	}
	if !f.all[f.cur] {
		t.Fatal("the current client was closed")
	}
}
