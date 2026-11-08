package appconfig

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

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
	var current []string
	for _, m := range core.SystemMigrations.Items() {
		current = append(current, m.File) // PocketBase's own migrations are Go
	}
	for _, e := range entries {
		current = append(current, e.Name())
	}
	for name, path := range map[string]string{
		"empty":   migrationsDB(t),
		"current": migrationsDB(t, current...),
	} {
		if err := Runtime.CheckDatabase(path, "project.db"); err != nil {
			t.Errorf("%s database refused: %v", name, err)
		}
	}
	for name, files := range map[string][]string{
		"multi-account": {"1800000000_users.js"},
		"newer":         append(append([]string{}, current...), "1890000000_more.js"),
		"foreign go":    {"1000000000_other.go"},
	} {
		err := Runtime.CheckDatabase(migrationsDB(t, files...), "project.db")
		if !errors.Is(err, appserver.ErrEarlierRelease) {
			t.Errorf("%s database accepted: %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "different "+Identity.DisplayName+" release") {
			t.Errorf("%s: unclear refusal: %v", name, err)
		}
	}
}
