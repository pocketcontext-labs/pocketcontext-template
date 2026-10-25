// Package tunnel carries one TemplateContext port between two computers through
// tailcat: WireGuard over Tailscale's data plane, bootstrapped through a DERP
// relay, without a Tailscale account or control plane. The agent computer serves
// exactly the project's port and proxies it to the server on 127.0.0.1; the
// person's computer listens on the same loopback port and dials it through the
// tunnel. Nothing else is reachable: no other ports, no forwarding through the
// server, no UDP.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/bundle"
)

// Identity is the server's tailcat identity. Keeping it makes the address
// stable across restarts; anyone holding the address can reach the served port,
// so the file is as private as the address.
type Identity struct {
	Key          key.NodePrivate      `json:"key"`
	PresharedKey tailcat.PresharedKey `json:"presharedKey"`
	// Region is the bootstrap relay chosen on the first start. The address
	// embeds its details, so it is reused to keep the address the same; when
	// that relay is retired, --new-address picks the nearest one again.
	Region *tailcfg.DERPRegion `json:"region,omitempty"`
}

// LoadIdentity reads the identity at path, or creates and saves a new one when
// the file is missing or rotate is set.
func LoadIdentity(path string, rotate bool) (*Identity, error) {
	if !rotate {
		data, err := os.ReadFile(path)
		if err == nil {
			var id Identity
			if err := json.Unmarshal(data, &id); err != nil || id.Key.IsZero() || id.PresharedKey.IsZero() {
				return nil, fmt.Errorf("the tunnel identity %s is damaged; run `templatecontext tunnel serve --new-address`", path)
			}
			return &id, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	id := &Identity{Key: key.NewNode(), PresharedKey: tailcat.NewPresharedKey()}
	return id, id.Save(path)
}

// Save writes the identity to a private file.
func (id *Identity) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return bundle.WriteAtomic(path, append(data, '\n'), 0600)
}

// quiet discards tailcat's and the data plane's diagnostics, which name keys
// and endpoints and would otherwise go to the standard logger.
func quiet(string, ...any) {}

// Serve exposes port on 127.0.0.1 through tailcat until ctx ends. ready
// receives the address once the server is up. A new identity gets the nearest
// public relay, recorded in id.Region for the caller to save.
func Serve(ctx context.Context, port int, id *Identity, ready func(tailcat.Addr)) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}
	served := uint16(port)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	s := &tailcat.Server{
		Key:            id.Key,
		PresharedKey:   id.PresharedKey,
		Logf:           quiet,
		Region:         id.Region,
		ServedTCPPorts: []filter.PortRange{{First: served, Last: served}},
		OnTCP: func(p uint16) func(net.Conn) {
			if p != served {
				return nil
			}
			return func(c net.Conn) {
				local, err := net.DialTimeout("tcp", target, 10*time.Second)
				if err != nil {
					c.Close()
					return
				}
				tailcat.ProxyConns(c, local)
			}
		},
	}
	if err := s.Start(); err != nil {
		s.Close()
		return fmt.Errorf("the tunnel did not start: %v", err)
	}
	defer s.Close()
	addr := s.TailcatAddr()
	if id.Region == nil {
		if info, err := tailcat.ParseAddr(addr); err == nil && len(info.Region) == 1 {
			id.Region = info.Region[0]
		}
	}
	ready(addr)
	<-ctx.Done()
	return nil
}

// ParseAddress validates a tailcat address without echoing it in errors: the
// address is the capability to reach the served port.
func ParseAddress(text string) (tailcat.Addr, error) {
	addr := tailcat.Addr(text)
	if _, err := tailcat.ParseAddr(addr); err != nil {
		return "", errors.New("that is not a valid tailcat address; copy the address printed by `templatecontext tunnel serve` on the agent computer")
	}
	return addr, nil
}

// Listen opens the loopback listener for Forward. A busy port is an error:
// the server only accepts its own port, so another one would not work.
func Listen(host string, port int) (net.Listener, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s (%v); the forward needs this exact port, so stop whatever uses it or ask its owner, then retry", address, err)
	}
	return ln, nil
}

// Forward accepts connections on ln and carries each one to the same port on
// the server at addr, until ctx ends.
func Forward(ctx context.Context, addr tailcat.Addr, ln net.Listener, port int) error {
	f := newForwarder(addr)
	defer f.close()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			remote, done, err := f.dial(ctx, uint16(port))
			if err != nil {
				if ctx.Err() == nil {
					io.WriteString(os.Stderr, "templatecontext: the agent computer did not answer through the tunnel; check that `templatecontext tunnel serve` runs there\n")
				}
				conn.Close()
				return
			}
			defer done()
			tailcat.ProxyConns(conn, remote)
		}()
	}
}

// forwarder holds the tailcat client of a forward. A client announces itself
// to the server only on its first dial, so after `tunnel serve` restarts (same
// address, no peers) the old client's packets are dropped for good. A failed
// dial therefore replaces the client, and the new one announces itself again.
// A replaced client stays open until the connections it still carries end, so
// one slow dial does not cut healthy connections.
type forwarder struct {
	addr tailcat.Addr
	mu   sync.Mutex
	cur  *session
	all  map[*session]bool // every client not yet closed
}

// session is one tailcat client and the dials and connections using it.
type session struct {
	c       *tailcat.Client
	active  int
	retired bool
}

func newForwarder(addr tailcat.Addr) *forwarder {
	f := &forwarder{addr: addr, all: map[*session]bool{}}
	f.cur = f.newSession()
	return f
}

// newSession starts a client; f.mu must be held or f not yet shared.
func (f *forwarder) newSession() *session {
	s := &session{c: &tailcat.Client{Server: f.addr, Logf: quiet}}
	f.all[s] = true
	return s
}

// acquire returns the current session, counted as in use until release.
func (f *forwarder) acquire() *session {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cur.active++
	return f.cur
}

// release ends one use of s, closing it once it is replaced and unused.
func (f *forwarder) release(s *session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.active--
	if s.retired && s.active == 0 && f.all[s] {
		delete(f.all, s)
		s.c.Close()
	}
}

// replace retires s in favour of a new session, unless another failed dial
// already replaced it, so concurrent failures rebuild the client once.
func (f *forwarder) replace(s *session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != s {
		return
	}
	s.retired = true
	f.cur = f.newSession()
}

// close closes every client, including replaced ones still carrying connections.
func (f *forwarder) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s := range f.all {
		s.c.Close()
	}
	clear(f.all)
}

// dial reaches port on the server, replacing the client and retrying once
// when the first attempt fails, so the connection that found the stale client
// still gets through. done must be called when the connection ends.
func (f *forwarder) dial(ctx context.Context, port uint16) (net.Conn, func(), error) {
	s := f.acquire()
	first, cancel := context.WithTimeout(ctx, 10*time.Second)
	remote, err := s.c.DialTCPPort(first, port)
	cancel()
	if err == nil {
		return remote, func() { f.release(s) }, nil
	}
	f.replace(s)
	f.release(s)
	if ctx.Err() != nil {
		return nil, nil, err
	}
	s = f.acquire()
	again, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err = s.c.DialTCPPort(again, port)
	if err != nil {
		f.release(s)
		return nil, nil, err
	}
	return remote, func() { f.release(s) }, nil
}
