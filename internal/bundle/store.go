package bundle

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/sqliteuri"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The synced database is never opened by the server. It is copied into the
// cache on start, and consistent snapshots are written back with VACUUM INTO
// and an atomic rename. A sync client therefore only ever sees whole files.

// fileState identifies the synced file this machine last copied or wrote.
type fileState struct {
	Size   int64     `json:"size"`
	Mtime  time.Time `json:"mtime"`
	SHA256 string    `json:"sha256"`
}

type storeState struct {
	Base  *fileState `json:"base"`  // nil: no synced database existed
	Dirty bool       `json:"dirty"` // the working copy has unsaved changes
	// Conflict is the copy that preserves this computer's changes after the
	// synced file was changed elsewhere; later write-backs refresh it.
	Conflict string `json:"conflict,omitempty"`
}

// Store owns the working copy of one project's database.
type Store struct {
	p       *Project
	mu      sync.Mutex
	state   storeState
	changes uint64 // bumped on every committed model change
	saved   uint64
	last    time.Time
}

func (s *Store) statePath() string { return filepath.Join(s.p.LiveDir(), "state.json") }
func (s *Store) livePath() string  { return filepath.Join(s.p.LiveDir(), "data.db") }

func (s *Store) loadState() error {
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.state)
}

func (s *Store) saveState() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	return WriteAtomic(s.statePath(), data, 0600)
}

// current describes the synced file, hashing only when size or time changed.
func (s *Store) current() (*fileState, error) {
	info, err := os.Stat(s.p.DB)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if b := s.state.Base; b != nil && b.Size == info.Size() && b.Mtime.Equal(info.ModTime()) {
		return b, nil
	}
	sum, err := fileSHA256(s.p.DB)
	if err != nil {
		return nil, err
	}
	return &fileState{Size: info.Size(), Mtime: info.ModTime(), SHA256: sum}, nil
}

func sameFile(a, b *fileState) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SHA256 == b.SHA256
}

// Open prepares the working copy. Unsaved changes left by a crash are written
// back first unless the synced file changed meanwhile, which needs a decision.
func (s *Store) Open() error {
	if err := os.MkdirAll(s.p.LiveDir(), 0700); err != nil {
		return err
	}
	if err := s.loadState(); err != nil {
		return fmt.Errorf("working copy state: %w", err)
	}
	synced, err := s.current()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(s.livePath())
	haveLive := statErr == nil
	if haveLive && s.state.Dirty {
		// Unsaved changes of another release stay where they are, untouched.
		if err := checkDatabase(s.livePath(), s.p.DB); err != nil {
			return err
		}
		if !sameFile(synced, s.state.Base) {
			return fmt.Errorf("%s changed (probably on another computer) while this computer had unsaved changes from an interrupted run; "+
				"run `templatecontext recover --keep local` or `--keep synced`", s.p.DB)
		}
		// Recover the interrupted run before anything else touches the files.
		s.changes = 1
		if err := s.writeBack(); err != nil {
			return fmt.Errorf("recovering unsaved changes: %w", err)
		}
		return nil
	}
	if haveLive && sameFile(synced, s.state.Base) {
		return nil // the working copy already matches the synced file
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(s.livePath() + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if synced != nil {
		if err := copyFile(s.p.DB, s.livePath()); err != nil {
			return fmt.Errorf("copying %s: %w", s.p.DB, err)
		}
	}
	s.state = storeState{Base: synced}
	return s.saveState()
}

// Recover resolves an interrupted run whose synced file also changed.
func (s *Store) Recover(keepLocal bool) error {
	if err := s.loadState(); err != nil {
		return err
	}
	if !s.state.Dirty {
		return errors.New("nothing to recover: the working copy has no unsaved changes")
	}
	if keepLocal {
		synced, err := s.current()
		if err != nil {
			return err
		}
		if synced != nil {
			backup := fmt.Sprintf("%s.replaced-%s.db", s.p.DB, time.Now().UTC().Format("20060102T150405Z"))
			if err := copyFile(s.p.DB, backup); err != nil {
				return err
			}
		}
		s.state.Base = synced
		s.changes = 1
		return s.writeBack()
	}
	conflict := s.state.Conflict
	if conflict == "" {
		conflict = filepath.Join(s.p.Dir, conflictName(s.p.DB))
	}
	if err := s.snapshotTo(conflict); err != nil {
		return err
	}
	s.state = storeState{}
	if err := s.saveState(); err != nil {
		return err
	}
	return os.Remove(s.livePath())
}

// Changed records a committed change; it persists the dirty flag at once so a
// crash cannot lose track of unsaved work.
func (s *Store) Changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes++
	s.last = time.Now()
	if !s.state.Dirty {
		s.state.Dirty = true
		_ = s.saveState()
	}
}

// Flush writes back once changes have been quiet for the given interval.
func (s *Store) Flush(quiet time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changes == s.saved || time.Since(s.last) < quiet {
		return nil
	}
	return s.writeBack()
}

// Close performs the final write-back.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changes == s.saved && !s.state.Dirty {
		return nil
	}
	return s.writeBack()
}

func conflictName(db string) string {
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	return fmt.Sprintf("%s.conflict-%s-%s-%s.db", filepath.Base(db), safeName(host), time.Now().UTC().Format("20060102T150405Z"), RandomHex(2))
}

func safeName(s string) string {
	out := []rune{}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "host"
	}
	return string(out)
}

// snapshot writes a consistent copy of the working database to dst.
func (s *Store) snapshot(dst string) error {
	// Read-write: a WAL database without its -shm file cannot be opened read-only.
	u := sqliteuri.File(s.livePath())
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=10000"); err != nil {
		return err
	}
	_, err = db.Exec("VACUUM INTO ?", dst)
	return err
}

// snapshotTo atomically replaces dst with a consistent copy of the working database.
func (s *Store) snapshotTo(dst string) error {
	temp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(s.p.DB)+".tmp-"+RandomHex(4))
	defer os.Remove(temp)
	if err := s.snapshot(temp); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if f, err := os.Open(temp); err == nil {
		_ = f.Sync()
		f.Close()
	}
	return renameRetry(temp, dst)
}

// writeBack must be called with s.mu held.
func (s *Store) writeBack() error {
	target := s.changes
	synced, err := s.current()
	if err != nil {
		return err
	}
	if !sameFile(synced, s.state.Base) {
		// Never overwrite a file another computer wrote; keep both and say so.
		if s.state.Conflict == "" {
			s.state.Conflict = filepath.Join(s.p.Dir, conflictName(s.p.DB))
		}
		if err := s.snapshotTo(s.state.Conflict); err != nil {
			return err
		}
		s.saved = target
		s.state.Dirty = true
		_ = s.saveState()
		return fmt.Errorf("%s changed on another computer; this computer's changes are kept in %s; run `templatecontext stop`, then `templatecontext recover`", s.p.DB, s.state.Conflict)
	}
	if err := s.snapshotTo(s.p.DB); err != nil {
		return err
	}
	written, err := s.current()
	if err != nil {
		return err
	}
	s.saved = target
	s.state = storeState{Base: written, Dirty: s.changes != target}
	return s.saveState()
}

// Lease marks the project as active on one computer. Cloud folders cannot
// share a SQLite lock, so a heartbeat file is the cross-machine guard.
type Lease struct {
	Host      string    `json:"host"`
	PID       int       `json:"pid"`
	Instance  string    `json:"instance"`
	Heartbeat time.Time `json:"heartbeat"`
}

// LeaseTimeout is how long a silent lease from another computer blocks starting.
const LeaseTimeout = 2 * time.Minute

func ReadLease(p *Project) (*Lease, error) {
	data, err := os.ReadFile(p.LeaseFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lease
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, nil // a partially synced lease counts as absent
	}
	return &l, nil
}

func writeLease(p *Project) error {
	host, _ := os.Hostname()
	data, _ := json.Marshal(Lease{Host: host, PID: os.Getpid(), Instance: p.Config.Instance, Heartbeat: time.Now().UTC()})
	return WriteAtomic(p.LeaseFile(), data, 0644)
}

// claimLease fails if another computer used the project recently.
func claimLease(p *Project, takeOver bool) error {
	l, err := ReadLease(p)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if l != nil && l.Host != host && time.Since(l.Heartbeat) < LeaseTimeout && !takeOver {
		return fmt.Errorf("the project is active on %s (last seen %s); stop it there, or start with --take-over if that computer is off",
			l.Host, l.Heartbeat.Local().Format(time.RFC822))
	}
	return writeLease(p)
}

func releaseLease(p *Project) {
	host, _ := os.Hostname()
	if l, _ := ReadLease(p); l != nil && l.Host == host && l.PID == os.Getpid() {
		_ = os.Remove(p.LeaseFile())
	}
}
