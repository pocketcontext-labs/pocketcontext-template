package client

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/bundle"
)

// Remote client mode: the server runs on another computer (the agent
// computer) and this computer reaches it through a port forward on the same
// loopback port. It never resolves or creates a database, never starts a
// server and never reads the local key; it signs in with a one-time code issued
// on the agent computer (or the password) and keeps only the resulting
// token in a private session file.

// refreshAfter is how old a stored token may be before it is refreshed. Tokens
// are valid for seven days.
const refreshAfter = 24 * time.Hour

// session is the stored sign-in for one server URL. It never holds a password.
type session struct {
	URL       string    `json:"url"`
	Instance  string    `json:"instance"`
	Token     string    `json:"token"`
	Refreshed time.Time `json:"refreshed"`
}

// normalizeURL accepts only http://127.0.0.1:PORT or http://localhost:PORT.
func normalizeURL(raw string) (string, error) {
	bad := fail(2, "--url must be http://127.0.0.1:PORT or http://localhost:PORT; only loopback URLs are accepted (forward the server's port with `templatecontext tunnel forward` or SSH)")
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", bad
	}
	if u.Path != "" && u.Path != "/" {
		return "", bad
	}
	host, port := u.Hostname(), u.Port()
	if host != "127.0.0.1" && host != "localhost" {
		return "", bad
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", bad
	}
	return "http://" + host + ":" + port, nil
}

// sessionPath is the private session file for a normalized URL.
func sessionPath(base string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no user cache directory: %w", err)
	}
	sum := sha256.Sum256([]byte(base))
	return filepath.Join(cache, "templatecontext", "remote-"+hex.EncodeToString(sum[:])[:16], "session.json"), nil
}

// loadSession returns the stored session, or nil when there is none.
func loadSession(base string) (*session, error) {
	path, err := sessionPath(base)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil || s.Token == "" || s.URL != base {
		return nil, fail(2, "the session file %s is damaged; run `templatecontext login --url %s`", path, base)
	}
	return &s, nil
}

func saveSession(s *session) error {
	path, err := sessionPath(s.URL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0700)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return bundle.WriteAtomic(path, append(data, '\n'), 0600)
}

func deleteSession(base string) error {
	path, err := sessionPath(base)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// probe asks the remote URL which server answers. It never starts anything.
func probe(base string) (*bundle.Instance, error) {
	quick := &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect}
	resp, err := quick.Get(base + "/api/bundle/instance")
	if err != nil {
		return nil, fail(1, "the TemplateContext server at %s is not reachable. The server runs on the agent computer: check that it runs there and that the forward of the same port is active (for example `templatecontext tunnel forward --address-stdin --url %s`, or `ssh -L %s:127.0.0.1:%s`). Nothing was started here.",
			base, base, port(base), port(base))
	}
	defer resp.Body.Close()
	var i bundle.Instance
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&i) != nil || i.App != "templatecontext" {
		return nil, fail(1, "%s is answered by another program, not TemplateContext (HTTP %d). Forward the server's own port to the same local port; the server refuses other ports.", base, resp.StatusCode)
	}
	return &i, nil
}

func port(base string) string {
	_, p, _ := strings.Cut(strings.TrimPrefix(base, "http://"), ":")
	return p
}

// checkRemote probes the server and compares it with the stored session.
func checkRemote(base string, s *session) (*bundle.Instance, error) {
	i, err := probe(base)
	if err != nil {
		return nil, err
	}
	if s != nil && s.Instance != i.Instance {
		return nil, fail(1, "%s now serves a different TemplateContext than the one you signed in to; sign in again: %s", base, signInHint(base))
	}
	return i, nil
}

// signInHint says how to sign in to base.
func signInHint(base string) string {
	return fmt.Sprintf("get a one-time code on the agent computer (`templatecontext login-code` there), then run `templatecontext login --url %s --code-stdin` and write the code to standard input; or sign in with the password: `templatecontext login --url %s`", base, base)
}

func expired(base string) error {
	return fail(2, "session expired; %s", signInHint(base))
}

// NewRemote returns a client for a server reached through a port forward,
// using the stored session.
func NewRemote(base string) (*Client, error) {
	c := newClient(base)
	s, err := loadSession(base)
	if err != nil {
		return nil, err
	}
	if _, err := checkRemote(base, s); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fail(2, "not signed in to %s; %s", base, signInHint(base))
	}
	c.remote, c.token = true, s.Token
	if time.Since(s.Refreshed) > refreshAfter {
		status, data, err := c.send("POST", "/api/collections/users/auth-refresh", nil, nil, "", nil)
		if err != nil {
			return nil, err
		}
		token, _ := dig(data, "token").(string)
		if status != 200 || token == "" {
			return nil, fail(1, "refreshing the session failed (HTTP %d): %s", status, message(data))
		}
		s.Token, s.Refreshed, c.token = token, time.Now().UTC(), token
		if err := saveSession(s); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// login signs in with the password and stores only the token.
func login(base string, passwordStdin bool) (map[string]any, error) {
	i, err := checkRemote(base, nil)
	if err != nil {
		return nil, err
	}
	password, err := readPassword(passwordStdin, false)
	if err != nil {
		return nil, err
	}
	c := newClient(base)
	status, data, err := c.send("POST", "/api/collections/users/auth-with-password", map[string]string{"identity": bundle.AccountEmail, "password": password}, nil, "", nil)
	if err != nil {
		return nil, err
	}
	token, _ := dig(data, "token").(string)
	if status != 200 || token == "" {
		return nil, fail(1, "sign-in to %s failed (HTTP %d): %s", base, status, message(data))
	}
	if err := saveSession(&session{URL: base, Instance: i.Instance, Token: token, Refreshed: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return map[string]any{"url": base, "instance": i.Instance}, nil
}

// readCode reads a one-time sign-in code from the first line of standard input.
func readCode() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return "", fail(2, "no sign-in code on standard input; usage: printf '%%s\\n' \"$CODE\" | templatecontext login --url URL --code-stdin")
	}
	return code, nil
}

// loginWithCode redeems a one-time code issued on the agent computer and
// stores only the resulting token.
func loginWithCode(base string) (map[string]any, error) {
	code, err := readCode()
	if err != nil {
		return nil, err
	}
	i, err := checkRemote(base, nil)
	if err != nil {
		return nil, err
	}
	c := newClient(base)
	status, data, err := c.send("POST", "/api/bundle/redeem", map[string]string{"code": code}, nil, "", nil)
	if err != nil {
		return nil, err
	}
	token, _ := dig(data, "token").(string)
	if status != 200 || token == "" {
		return nil, fail(1, "sign-in to %s failed (HTTP %d): %s Ask for a new code: `templatecontext login-code` on the agent computer.", base, status, message(data))
	}
	if err := saveSession(&session{URL: base, Instance: i.Instance, Token: token, Refreshed: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return map[string]any{"url": base, "instance": i.Instance}, nil
}

// remoteStatus reports reachability and sign-in without failing when unreachable.
func remoteStatus(base string) (map[string]any, error) {
	s, err := loadSession(base)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"url": base, "reachable": false, "instance": "", "loggedIn": false}
	i, probeErr := probe(base)
	if probeErr == nil {
		out["reachable"], out["instance"] = true, i.Instance
	} else {
		out["error"] = probeErr.Error()
	}
	out["loggedIn"] = s != nil && (i == nil || i.Instance == s.Instance)
	return out, nil
}
