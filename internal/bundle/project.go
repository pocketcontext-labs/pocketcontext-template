// Package bundle runs TemplateContext as one local binary: a project is a SQLite
// file, its storage folder and an optional configuration file, all of which may
// live in a cloud-synced folder. Everything else stays in a per-machine cache.
package bundle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultPort is TemplateContext's dedicated loopback port, distinct from the
// other Context applications so several can run at the same time.
const DefaultPort = 8098

// DefaultDatabase is the database file name used when only a folder is known.
const DefaultDatabase = "templatecontext.db"

// Config is the optional templatecontext.json beside the database. It is synced
// with the project, so it never contains credentials or machine-specific paths.
type Config struct {
	Port     int    `json:"port"`
	Instance string `json:"instance,omitempty"`
}

// Project is one TemplateContext database and the paths derived from it.
type Project struct {
	DB         string // absolute path of the synced database file
	Dir        string // folder holding the database, storage/ and templatecontext.json
	ConfigPath string
	Cache      string // private per-machine cache for this database
	Key        string // stable per-machine identifier of this database path
	Config     Config
}

// Resolve finds the project from an explicit path, TEMPLATECONTEXT_DB, or the
// templatecontext.db beside the executable, in that order. A directory selects its templatecontext.db.
func Resolve(db string) (*Project, error) {
	if db == "" {
		db = os.Getenv("TEMPLATECONTEXT_DB")
	}
	if db == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, errors.New("cannot locate the executable; pass --db")
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		db = filepath.Join(filepath.Dir(exe), DefaultDatabase)
	}
	abs, err := filepath.Abs(db)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		abs = filepath.Join(abs, DefaultDatabase)
	}
	p := &Project{DB: abs, Dir: filepath.Dir(abs)}
	p.ConfigPath = filepath.Join(p.Dir, "templatecontext.json")
	identity := abs
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		identity = strings.ToLower(identity) // case-insensitive filesystems
	}
	sum := sha256.Sum256([]byte(identity))
	p.Key = hex.EncodeToString(sum[:8])
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("no user cache directory: %w", err)
	}
	p.Cache = filepath.Join(base, "templatecontext", p.Key)
	p.Config = Config{Port: DefaultPort}
	if err := p.loadConfig(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Project) loadConfig() error {
	data, err := os.ReadFile(p.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("%s: %w", p.ConfigPath, err)
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%s: port must be 1024-65535", p.ConfigPath)
	}
	p.Config = c
	return nil
}

// SaveConfig writes templatecontext.json atomically.
func (p *Project) SaveConfig() error {
	data, err := json.MarshalIndent(p.Config, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(p.ConfigPath, append(data, '\n'), 0644)
}

// EnsureInstance gives the project a stable identifier shared by every computer.
func (p *Project) EnsureInstance() error {
	if p.Config.Instance != "" {
		return nil
	}
	p.Config.Instance = RandomHex(16)
	return p.SaveConfig()
}

func (p *Project) URL() string       { return fmt.Sprintf("http://127.0.0.1:%d", p.Config.Port) }
func (p *Project) LiveDir() string   { return filepath.Join(p.Cache, "live") }
func (p *Project) KeyFile() string   { return filepath.Join(p.Cache, "local.key") }
func (p *Project) LogFile() string   { return filepath.Join(p.Cache, "server.log") }
func (p *Project) LeaseFile() string { return p.DB + ".lease" }
func (p *Project) StorageDir() string {
	return filepath.Join(p.Dir, "storage")
}

// RandomHex returns n random bytes as hexadecimal.
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// WriteAtomic replaces path through a temporary file in the same folder.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(temp, mode); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return renameRetry(temp, path)
}
