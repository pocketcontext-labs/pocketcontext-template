package bundle

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	assets "github.com/pocketcontext-labs/pocketcontext-template"
)

func migrationsDB(t *testing.T, files ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if files == nil {
		if _, err := db.Exec("CREATE TABLE other(id)"); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := db.Exec("CREATE TABLE _migrations(file TEXT PRIMARY KEY, applied INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := db.Exec("INSERT INTO _migrations VALUES (?, 0)", f); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestCheckDatabaseRefusesOtherReleases: a database whose schema came from
// migrations this release does not ship, such as the multi-account schema of
// template releases before the single-user one, is refused before PocketBase
// opens it; a new or current database is accepted.
func TestCheckDatabaseRefusesOtherReleases(t *testing.T) {
	entries, err := fs.ReadDir(assets.Assets, "pb_migrations")
	if err != nil || len(entries) == 0 {
		t.Fatal("no embedded migrations", err)
	}
	current := []string{"1640988000_init.go"} // PocketBase's own migrations are Go
	for _, e := range entries {
		current = append(current, "pb_migrations/"+e.Name())
	}
	for name, path := range map[string]string{
		"empty":   migrationsDB(t),
		"current": migrationsDB(t, current...),
	} {
		if err := checkDatabase(path, "project.db"); err != nil {
			t.Errorf("%s database refused: %v", name, err)
		}
	}
	for name, files := range map[string][]string{
		"multi-account": {"1640988000_init.go", "1800000000_users.js"},
		"newer":         append(current, "1890000000_more.js"),
	} {
		err := checkDatabase(migrationsDB(t, files...), "project.db")
		if !errors.Is(err, ErrUnsupportedDatabase) {
			t.Errorf("%s database accepted: %v", name, err)
		}
	}
}
