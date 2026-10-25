package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Instance is what /api/bundle/instance reports.
type Instance struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Instance string `json:"instance"`
	Key      string `json:"key"`
}

var quick = &http.Client{Timeout: 2 * time.Second}

// errNotRunning means nothing answers on the project's port.
var errNotRunning = errors.New("not running")

// Health asks the project's port which server answers.
func Health(p *Project) (*Instance, error) {
	resp, err := quick.Get(p.URL() + "/api/bundle/instance")
	if err != nil {
		return nil, errNotRunning
	}
	defer resp.Body.Close()
	var i Instance
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&i) != nil || i.App != "templatecontext" {
		return nil, fmt.Errorf("port %d is used by another program; set a different \"port\" in %s", p.Config.Port, p.ConfigPath)
	}
	return &i, nil
}

// Running reports whether this project's own server answers.
func Running(p *Project) (bool, error) {
	i, err := Health(p)
	if errors.Is(err, errNotRunning) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if i.Key != p.Key || (p.Config.Instance != "" && i.Instance != p.Config.Instance) {
		return false, fmt.Errorf("port %d is used by another TemplateContext database; set a different \"port\" in %s", p.Config.Port, p.ConfigPath)
	}
	return true, nil
}

// StartOptions control EnsureRunning.
type StartOptions struct {
	TakeOver bool
}

// EnsureRunning starts the server in the background unless it already runs,
// then waits until it answers. It is safe to call before every command.
func EnsureRunning(p *Project, opts StartOptions) error {
	ok, err := Running(p)
	if err != nil || ok {
		return err
	}
	if err := os.MkdirAll(p.Cache, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(p.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	start, _ := logFile.Seek(0, io.SeekEnd)
	args := []string{"serve", "--db", p.DB}
	if opts.TakeOver {
		args = append(args, "--take-over")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Dir = p.Cache
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("the TemplateContext server stopped during start: %s (log: %s)", lastLines(p.LogFile(), start), p.LogFile())
		case <-time.After(200 * time.Millisecond):
		}
		if err := p.loadConfig(); err != nil { // serve may have just created the instance ID
			return err
		}
		if ok, err := Running(p); err != nil || ok {
			if ok {
				_ = cmd.Process.Release()
			}
			return err
		}
	}
	return fmt.Errorf("the TemplateContext server did not answer within 60 seconds; see %s", p.LogFile())
}

func lastLines(path string, from int64) string {
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) < from {
		return "unknown error"
	}
	lines := strings.Split(strings.TrimSpace(string(data[from:])), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}

// ReadKey returns the running server's local key.
func ReadKey(p *Project) (string, error) {
	data, err := os.ReadFile(p.KeyFile())
	if err != nil {
		return "", errors.New("the local server key is missing; is the server running for this database?")
	}
	return strings.TrimSpace(string(data)), nil
}

// LocalCall sends a key-authenticated request to the running server.
func LocalCall(p *Project, path string, body any) (map[string]any, error) {
	key, err := ReadKey(p)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", p.URL()+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(KeyHeader, key)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]any
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data)
	if resp.StatusCode != 200 {
		msg, _ := data["message"].(string)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return data, nil
}

// Stop asks the server to write back and exit, then waits until it has.
func Stop(p *Project) error {
	ok, err := Running(p)
	if err != nil || !ok {
		return err
	}
	if _, err := LocalCall(p, "/api/bundle/stop", map[string]any{}); err != nil {
		return err
	}
	lock := p.Cache + string(os.PathSeparator) + "server.lock"
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if f, err := os.OpenFile(lock, os.O_RDWR, 0600); err == nil {
			if lockFile(f) == nil { // the server released its lock: it has exited
				f.Close()
				return nil
			}
			f.Close()
		}
	}
	return errors.New("the server did not stop within 30 seconds")
}

// OpenBrowser opens url with the platform's default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// Recover resolves an interrupted run; the server must not be running.
func Recover(p *Project, keepLocal bool) error {
	if ok, err := Running(p); err != nil || ok {
		if ok {
			return errors.New("stop the server first")
		}
		return err
	}
	return (&Store{p: p}).Recover(keepLocal)
}

// Conflicts lists copies that preserve changes made while another computer
// replaced the synced database. They need the user's review.
func Conflicts(p *Project) []string {
	matches, _ := filepath.Glob(p.DB + ".conflict-*.db")
	return matches
}
