package searchread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/sqlread"
)

func sourceContentFixture(t *testing.T) (*sql.DB, *PrivateEngine) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
 CREATE TABLE spaces(id TEXT PRIMARY KEY);
 CREATE TABLE space_members(account TEXT,space TEXT,active INTEGER);
 CREATE TABLE sources(id TEXT PRIMARY KEY,space TEXT,title TEXT,original_name TEXT,source_date TEXT);
 CREATE TABLE renditions(id TEXT PRIMARY KEY,space TEXT,source TEXT);
 CREATE TABLE passages(id TEXT PRIMARY KEY,space TEXT,rendition TEXT,body TEXT);
 INSERT INTO spaces VALUES('s1'),('s2');
 INSERT INTO space_members VALUES('alice','s1',1),('bob','s2',1),('both','s1',1),('both','s2',1);
 INSERT INTO sources VALUES('a','s1','Rocket metadata','unextracted.md','2026'),('b','s1','Uncited notes','notes.md',''),('c','s1','Another note','other.md',''),('hidden','s2','Rocket private','secret.md','');
 INSERT INTO renditions VALUES('rb','s1','b'),('rc','s1','c'),('rh','s2','hidden');
 INSERT INTO passages VALUES('pb1','s1','rb','A rocket launches to café orbit.'),('pb2','s1','rb','Another rocket. <img src=x onerror=unsafe()>'),('pc','s1','rc','Rocket exploration report.'),('ph','s2','rh','Rocket hiddenword private body.');`)
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string][]string{}
	filters := map[string]string{}
	for _, f := range SourceContentFields() {
		tables[f[0]] = f[1:]
		field := f[0] + ".space"
		if f[0] == "spaces" {
			field = "spaces.id"
		}
		filters[f[0]] = fmt.Sprintf("EXISTS (SELECT 1 FROM space_members m WHERE m.account=:requester AND m.active=1 AND m.space=%s)", field)
	}
	cfg := Config{Indexes: map[string]Index{"sources": {Isolation: IsolationPrivate, SourceContent: true, Collection: "sources", Columns: []string{"title", "original_name", "source_date", "body"}, SnippetColumn: "body", Weights: []float64{8, 4, 2, 1}, Scope: &Scope{Collection: "spaces", Field: "id"}}}}
	e, _ := newPrivate(t, path, sqlread.Config{Tables: tables, Timeout: time.Second, MaxRows: 100, MaxBytes: 1 << 20}, sqlread.SnapshotConfig{Filters: filters, PolicyTables: map[string][]string{"space_members": {"account", "space", "active"}}}, cfg)
	return db, e
}
func TestSourceContentGroupedRankedPagination(t *testing.T) {
	_, e := sourceContentFixture(t)
	ctx := context.Background()
	request := Request{Index: "sources", Scope: "s1", Query: "rocket", Limit: 1}
	first, err := e.SearchAs(ctx, "alice", request)
	if err != nil {
		t.Fatal(err)
	}
	if ids(first) != "a" || first.Hits[0].Passage != "" || !first.HasMore {
		t.Fatalf("metadata weighting/no invented passage: %+v", first)
	}
	found := map[string]bool{"a": true}
	for offset := 1; offset < 3; offset++ {
		request.Offset = offset
		request.ExpectedGeneration = first.Generation
		r, err := e.SearchAs(ctx, "alice", request)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) != 1 || found[r.Hits[0].ID] || r.Hits[0].Passage == "" {
			t.Fatalf("grouped page: %+v", r)
		}
		found[r.Hits[0].ID] = true
		if r.HasMore != (offset < 2) {
			t.Fatalf("more: %+v", r)
		}
	}
	if !found["b"] || !found["c"] {
		t.Fatal(found)
	}
	r, err := e.SearchAs(ctx, "alice", Request{Index: "sources", Scope: "s1", Query: "café"})
	if err != nil || ids(r) != "b" || r.Hits[0].Passage != "pb1" || !strings.Contains(r.Hits[0].Excerpt, "café") {
		t.Fatalf("uncited extracted body: %+v %v", r, err)
	}
	r, err = e.SearchAs(ctx, "alice", Request{Index: "sources", Scope: "s1", Query: "unextracted.md"})
	if err != nil || ids(r) != "a" || r.Hits[0].Passage != "" {
		t.Fatalf("filename: %+v %v", r, err)
	}
}
func TestSourceContentIsolationGenerationAndRevocation(t *testing.T) {
	db, e := sourceContentFixture(t)
	ctx := context.Background()
	request := Request{Index: "sources", Scope: "s1", Query: "rocket", Limit: 1}
	first, err := e.SearchAs(ctx, "alice", request)
	if err != nil {
		t.Fatal(err)
	}
	both, err := e.SearchAs(ctx, "both", request)
	if err != nil || first.Generation != both.Generation || ids(first) != ids(both) {
		t.Fatalf("other readable space affects corpus: %+v %v", both, err)
	}
	if _, err = db.Exec(`UPDATE passages SET body='hidden changed body rocket' WHERE id='ph'`); err != nil {
		t.Fatal(err)
	}
	after, err := e.SearchAs(ctx, "both", request)
	if err != nil || after.Generation != first.Generation {
		t.Fatalf("other-space generation: %+v %v", after, err)
	}
	for _, scope := range []string{"s2", "missing"} {
		_, err = e.SearchAs(ctx, "alice", Request{Index: "sources", Scope: scope, Query: "rocket"})
		if !errors.Is(err, ErrScopeNotFound) {
			t.Fatalf("%s: %v", scope, err)
		}
	}
	request.Offset = 1
	request.ExpectedGeneration = first.Generation
	if _, err = db.Exec(`INSERT INTO passages VALUES('new','s1','rb','New rocket passage')`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.SearchAs(ctx, "alice", request); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("changed generation: %v", err)
	}
	if _, err = db.Exec(`UPDATE space_members SET active=0 WHERE account='alice'`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.SearchAs(ctx, "alice", request); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("cached revocation: %v", err)
	}
}
func TestSourceContentLiteralQueriesAndRequiredScope(t *testing.T) {
	_, e := sourceContentFixture(t)
	for _, q := range []string{"rocket OR hiddenword", `rocket" UNION SELECT`, "*"} {
		r, err := e.SearchAs(context.Background(), "alice", Request{Index: "sources", Scope: "s1", Query: q})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) != 0 {
			t.Fatalf("query syntax executed: %+v", r)
		}
	}
	if _, err := e.SearchAs(context.Background(), "alice", Request{Index: "sources", Query: "rocket"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}

func TestSourceContentConfigurationAndCapacity(t *testing.T) {
	good := Index{Isolation: IsolationPrivate, SourceContent: true, Collection: "sources", Columns: []string{"title", "original_name", "source_date", "body"}, SnippetColumn: "body", Scope: &Scope{Collection: "spaces", Field: "id"}}
	for _, change := range []func(*Index){
		func(i *Index) { i.Collection = "users" },
		func(i *Index) { i.Columns = []string{"password"} },
		func(i *Index) { i.Scope = &Scope{Collection: "spaces", Field: "name"} },
		func(i *Index) { i.Where = "1 OR 1=1" },
		func(i *Index) { i.Members = "visible" },
		func(i *Index) { i.Isolation = "" },
	} {
		bad := good
		change(&bad)
		if err := validatePrivateIndex(bad); err == nil {
			t.Fatalf("accepted unsafe shape: %+v", bad)
		}
	}
	if _, err := CanonicalDDL(good); err == nil {
		t.Fatal("source-content mode allowed in shared index")
	}
	db, e := sourceContentFixture(t)
	e.maxIndexBytes = 32
	if _, err := e.SearchAs(context.Background(), "alice", Request{Index: "sources", Scope: "s1", Query: "rocket"}); !errors.Is(err, ErrIndexLimit) {
		t.Fatalf("source index byte cap: %v", err)
	}
	e.maxIndexBytes = DefaultMaxIndexBytes
	if _, err := db.Exec(`DELETE FROM passages WHERE space='s1'; DELETE FROM renditions WHERE space='s1'; DELETE FROM sources WHERE space='s1'`); err != nil {
		t.Fatal(err)
	}
	r, err := e.SearchAs(context.Background(), "alice", Request{Index: "sources", Scope: "s1", Query: "rocket"})
	if err != nil || len(r.Hits) != 0 || r.HasMore {
		t.Fatalf("empty authorized scope: %+v %v", r, err)
	}
}
