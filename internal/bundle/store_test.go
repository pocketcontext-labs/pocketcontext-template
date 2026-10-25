package bundle

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testProject(t *testing.T) *Project {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "Cloud Drive", "TemplateContext")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return &Project{DB: filepath.Join(dir, "templatecontext.db"), Dir: dir, Cache: filepath.Join(root, "cache"), Config: Config{Port: DefaultPort}}
}

func execLive(t *testing.T, s *Store, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite3", s.livePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWriteBackAndReopen(t *testing.T) {
	p := testProject(t)
	s := &Store{p: p}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	execLive(t, s, "CREATE TABLE notes(x); INSERT INTO notes VALUES (1)")
	s.Changed()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, p.DB, "notes"); n != 1 {
		t.Fatalf("synced copy has %d rows", n)
	}
	// A new run reuses the matching working copy and sees no unsaved changes.
	s2 := &Store{p: p}
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	if s2.state.Dirty {
		t.Fatal("reopened working copy is dirty")
	}
	entries, _ := os.ReadDir(p.Dir)
	for _, e := range entries {
		if e.Name() != "templatecontext.db" {
			t.Errorf("unexpected file in project folder: %s", e.Name())
		}
	}
}

func TestSyncedFileReplacedElsewhere(t *testing.T) {
	p := testProject(t)
	s := &Store{p: p}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	execLive(t, s, "CREATE TABLE notes(x)")
	s.Changed()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Another computer's version arrives through the sync client.
	other := filepath.Join(t.TempDir(), "other.db")
	db, _ := sql.Open("sqlite3", other)
	if _, err := db.Exec("CREATE TABLE notes(x); INSERT INTO notes VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := copyFile(other, p.DB); err != nil {
		t.Fatal(err)
	}
	execLive(t, s, "INSERT INTO notes VALUES (99)")
	s.Changed()
	err := s.Close()
	if err == nil || !strings.Contains(err.Error(), "changed on another computer") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if n := count(t, p.DB, "notes"); n != 2 {
		t.Fatalf("the other computer's file was overwritten (%d rows)", n)
	}
	if n := count(t, s.state.Conflict, "notes"); n != 1 {
		t.Fatalf("conflict copy has %d rows", n)
	}
	// Restarting refuses until the user chooses.
	if err := (&Store{p: p}).Open(); err == nil || !strings.Contains(err.Error(), "recover") {
		t.Fatalf("expected recover instruction, got %v", err)
	}
	if err := (&Store{p: p}).Recover(false); err != nil {
		t.Fatal(err)
	}
	s3 := &Store{p: p}
	if err := s3.Open(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s3.livePath(), "notes"); n != 2 {
		t.Fatalf("working copy did not adopt the synced file (%d rows)", n)
	}
}

func TestLeaseFromAnotherComputer(t *testing.T) {
	p := testProject(t)
	if err := os.WriteFile(p.LeaseFile(), []byte(`{"host":"other-laptop","pid":1,"heartbeat":"2999-01-01T00:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := claimLease(p, false); err == nil || !strings.Contains(err.Error(), "other-laptop") {
		t.Fatalf("expected lease refusal, got %v", err)
	}
	if err := claimLease(p, true); err != nil {
		t.Fatal(err)
	}
	releaseLease(p)
	if _, err := os.Stat(p.LeaseFile()); !os.IsNotExist(err) {
		t.Fatal("lease not released")
	}
}

func TestResolveDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEMPLATECONTEXT_DB", "")
	p, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.DB != filepath.Join(dir, DefaultDatabase) || p.Config.Port != DefaultPort {
		t.Fatalf("resolved %+v", p)
	}
}
