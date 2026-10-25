package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	good := map[string]string{
		"http://127.0.0.1:8098":   "http://127.0.0.1:8098",
		"http://127.0.0.1:8098/":  "http://127.0.0.1:8098",
		"http://localhost:8098":   "http://localhost:8098",
		" http://localhost:9000/": "http://localhost:9000",
	}
	for in, want := range good {
		got, err := normalizeURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "127.0.0.1:8098", "https://127.0.0.1:8098", "http://127.0.0.1", "http://127.0.0.1:0",
		"http://127.0.0.1:70000", "http://127.0.0.1:09000", "http://example.com:8098", "http://10.0.0.1:8098", "http://[::1]:8098",
		"http://127.0.0.1:8098/api", "http://127.0.0.1:8098?x=1", "http://127.0.0.1:8098/?", "http://u:p@127.0.0.1:8098",
		"http://127.0.0.1:8098#x", "http://127.0.0.2:8098"} {
		_, err := normalizeURL(in)
		f, ok := err.(*Fail)
		if !ok || f.Code != 2 || !strings.Contains(f.Message, "loopback") {
			t.Errorf("normalizeURL(%q) = %v; want a usage failure", in, err)
		}
	}
}

func TestSessionFile(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("LocalAppData", cache)
	// A fixed URL, not the default port, so a derived product's port leaves this known answer intact.
	base := "http://127.0.0.1:9000"
	path, err := sessionPath(base)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("http://127.0.0.1:9000")[:16 hex]
	if filepath.Base(filepath.Dir(path)) != "remote-67d8ea34d836dea3" {
		t.Fatalf("unexpected session directory %s", path)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(path))) != "templatecontext" || filepath.Base(path) != "session.json" {
		t.Fatalf("unexpected session path %s", path)
	}
	if other, _ := sessionPath("http://localhost:9000"); other == path {
		t.Fatal("different URLs share a session")
	}
	if s, err := loadSession(base); s != nil || err != nil {
		t.Fatalf("loadSession without a file = %v, %v", s, err)
	}
	want := &session{URL: base, Instance: "abc", Email: "a@b.c", Token: "t", Refreshed: time.Now().UTC().Truncate(time.Second)}
	if err := saveSession(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSession(base)
	if err != nil || got == nil || *got != *want {
		t.Fatalf("loadSession = %+v, %v; want %+v", got, err, want)
	}
	if runtime.GOOS != "windows" {
		for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm() != mode {
				t.Errorf("%s mode = %v, %v; want %v", p, info.Mode().Perm(), err, mode)
			}
		}
	}
	if err := deleteSession(base); err != nil {
		t.Fatal(err)
	}
	if err := deleteSession(base); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if s, _ := loadSession(base); s != nil {
		t.Fatal("session survived deletion")
	}
}

func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

func TestReadCode(t *testing.T) {
	withStdin(t, "  abc123 \r\nignored\n")
	if code, err := readCode(); err != nil || code != "abc123" {
		t.Fatalf("readCode = %q, %v", code, err)
	}
	withStdin(t, "\n")
	if _, err := readCode(); err == nil || err.(*Fail).Code != 2 {
		t.Fatalf("empty code: %v", err)
	}
}

func TestCodeLoginUsage(t *testing.T) {
	// These fail before any network access or session file.
	for _, argv := range [][]string{
		{"login", "--url", "http://127.0.0.1:1", "--code-stdin", "--email", "a@example.localhost"},
		{"login", "--url", "http://127.0.0.1:1", "--code-stdin", "--password-stdin"},
		{"login-code", "--url", "http://127.0.0.1:1", "--email", "a@example.localhost"},
		{"open", "--url", "http://127.0.0.1:1", "--email", "a@example.localhost"},
	} {
		err := run(argv)
		f, ok := err.(*Fail)
		if !ok || f.Code != 2 {
			t.Errorf("%v: %v; want exit 2", argv, err)
		}
	}
}
