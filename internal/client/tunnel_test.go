package client

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/tailcat"
)

func validAddress() string {
	info := tailcat.NewPrivateKey().Public
	info.RegionID = 1
	return string(info.Addr())
}

func TestTunnelUsage(t *testing.T) {
	address := validAddress()
	missing := filepath.Join(t.TempDir(), "missing", "templatecontext.db")
	// These fail before any network access, listener or identity file.
	for _, argv := range [][]string{
		{"tunnel"},
		{"tunnel", "connect"},
		{"tunnel", "serve", "--url", "http://127.0.0.1:1"},
		{"tunnel", "serve", "extra", "--db", missing},
		{"tunnel", "serve", "--db", missing},
		{"tunnel", "forward"},
		{"tunnel", "forward", address, "--db", missing},
		{"tunnel", "forward", address, "--new-address"},
		{"tunnel", "forward", address, "--address-stdin"},
		{"tunnel", "forward", address, "--url", "http://example.com:8098"},
		{"tunnel", "forward", address, "--url", "http://0.0.0.0:8098"},
		{"tunnel", "forward", address, "--url", "https://127.0.0.1:8098"},
	} {
		err := run(argv)
		f, ok := err.(*Fail)
		if !ok || f.Code != 2 {
			t.Errorf("%v: %v; want exit 2", argv, err)
		}
	}
}

func TestTunnelForwardHidesInvalidAddress(t *testing.T) {
	err := run([]string{"tunnel", "forward", "tcSECRETbutBROKEN"})
	f, ok := err.(*Fail)
	if !ok || f.Code != 2 || strings.Contains(f.Message, "SECRET") {
		t.Fatalf("invalid address: %v", err)
	}
	withStdin(t, "tcSECRETbutBROKEN\n")
	err = run([]string{"tunnel", "forward", "--address-stdin"})
	if f, ok := err.(*Fail); !ok || f.Code != 2 || strings.Contains(f.Message, "SECRET") {
		t.Fatalf("invalid address on stdin: %v", err)
	}
}
