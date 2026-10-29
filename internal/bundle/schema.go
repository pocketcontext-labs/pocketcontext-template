package bundle

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	assets "github.com/pocketcontext-labs/pocketcontext-template"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/sqliteuri"
)

// ErrUnsupportedDatabase reports a database this release does not open.
var ErrUnsupportedDatabase = errors.New("unsupported database")

// checkDatabase refuses a working copy whose schema came from migrations this
// release does not ship (an earlier multi-account release of the template, a
// newer release, or another product) before PocketBase opens it, so no
// migration ever touches it. It only reads the working copy in the private
// cache (query-only, because a WAL file without its -shm cannot be opened
// read-only); the synced file is never opened.
func checkDatabase(db, synced string) error {
	u := sqliteuri.File(db)
	conn, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("PRAGMA query_only = 1"); err != nil {
		return fmt.Errorf("reading %s: %w", synced, err)
	}
	var tracked int
	if err := conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '_migrations'").Scan(&tracked); err != nil {
		return fmt.Errorf("reading %s: %w", synced, err)
	}
	if tracked == 0 {
		return nil // an empty database; PocketBase creates the schema
	}
	entries, err := fs.ReadDir(assets.Assets, "pb_migrations")
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, e := range entries {
		known[e.Name()] = true
	}
	rows, err := conn.Query("SELECT file FROM _migrations")
	if err != nil {
		return fmt.Errorf("reading %s: %w", synced, err)
	}
	defer rows.Close()
	for rows.Next() {
		var file string
		if err := rows.Scan(&file); err != nil {
			return err
		}
		// PocketBase's own migrations are Go code; the application's are pb_migrations/*.js.
		if strings.HasSuffix(file, ".js") && !known[path.Base(file)] {
			return fmt.Errorf("%w: %s was created by a different TemplateContext release (unknown schema migration %s), "+
				"for example an earlier multi-account one; this release does not open it and leaves the file unchanged. "+
				"Use the release that created it, or start a new project with `templatecontext init --db NEW_PATH`",
				ErrUnsupportedDatabase, synced, path.Base(file))
		}
	}
	return rows.Err()
}
