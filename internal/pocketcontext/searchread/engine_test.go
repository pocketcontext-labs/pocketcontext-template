package searchread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func fixture(t *testing.T, limits Limits) (*Engine, *sql.DB, Index, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	idx := Index{Table: "documents_search", Collection: "documents", Columns: []string{"title", "body"}, SnippetColumn: "body", Weights: []float64{3, 1}}
	ddl, err := CanonicalDDL(idx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE documents(id TEXT PRIMARY KEY,title TEXT,body TEXT);` + ddl + `; INSERT INTO documents VALUES('a','Alpha','alpha one'),('b','Beta','alpha two'),('c','Gamma','other'); INSERT INTO documents_search(record_id,title,body) SELECT id,title,body FROM documents;`); err != nil {
		t.Fatal(err)
	}
	e, err := New(path, Config{Indexes: map[string]Index{"docs": idx}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, db, idx, path
}
func TestSearch(t *testing.T) {
	e, _, _, _ := fixture(t, Limits{})
	r, err := e.Search(context.Background(), "docs", "alpha", 1)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].ID != "a" || !r.Truncated {
		t.Fatalf("%#v %v", r, err)
	}
	r, err = e.Search(context.Background(), "docs", "alpha two", 0)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].ID != "b" || r.Hits[0].Excerpt != "alpha two" {
		t.Fatalf("%#v %v", r, err)
	}
	for _, q := range []string{`alpha OR other`, `title:alpha`, `" OR "`, `*`, `NEAR(alpha)`, `'; DROP TABLE documents;--`} {
		if _, err = e.Search(context.Background(), "docs", q, 0); err != nil {
			t.Fatalf("literal %q: %v", q, err)
		}
	}
	for n := 0; n < 3; n++ {
		e.db.SetMaxIdleConns(0)
		if _, err = e.Search(context.Background(), "docs", "alpha", 0); err != nil {
			t.Fatalf("cold connection: %v", err)
		}
	}
}
func TestInvalidRequests(t *testing.T) {
	e, _, _, _ := fixture(t, Limits{})
	for _, q := range []string{"", "  ", strings.Repeat("x", MaxQueryBytes+1), strings.Repeat("x ", MaxTerms+1), "x\x00", string([]byte{255})} {
		if _, err := e.Search(context.Background(), "docs", q, 0); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%q: %v", q, err)
		}
	}
	for _, limit := range []int{-1, 101} {
		if _, err := e.Search(context.Background(), "docs", "x", limit); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
	if _, err := e.Search(context.Background(), "unknown", "x", 0); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}
func TestSchemaAndIDsFailClosed(t *testing.T) {
	for _, mutation := range []string{`INSERT INTO documents_search(record_id,title,body) VALUES('a','duplicate','alpha')`, `INSERT INTO documents_search(record_id,title,body) VALUES('','empty','alpha')`, `INSERT INTO documents_search(record_id,title,body) VALUES('orphan','missing','alpha')`, `DROP TABLE documents_search; CREATE TABLE documents_search(record_id,title,body)`, `ALTER TABLE documents_search_content ADD COLUMN private TEXT`} {
		t.Run(mutation, func(t *testing.T) {
			e, db, idx, path := fixture(t, Limits{})
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Search(context.Background(), "docs", "alpha", 0); !errors.Is(err, ErrIndexInvalid) {
				t.Fatalf("runtime %v", err)
			}
			if e2, err := New(path, Config{Indexes: map[string]Index{"docs": idx}}, Limits{}); err == nil {
				e2.Close()
				t.Fatal("startup accepted invalid index")
			}
		})
	}
}
func TestIndexLifecycle(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE documents SET body='replacement' WHERE id='a'; DELETE FROM documents_search WHERE record_id='a'; INSERT INTO documents_search(record_id,title,body) SELECT id,title,body FROM documents WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	r, err := e.Search(context.Background(), "docs", "alpha", 0)
	if err != nil || len(r.Hits) != 2 {
		t.Fatalf("rollback %#v %v", r, err)
	}
	tx, _ = db.Begin()
	if _, err = tx.Exec(`DELETE FROM documents_search; INSERT INTO documents_search(record_id,title,body) SELECT id,title,body FROM documents`); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if _, err = e.Search(context.Background(), "docs", "alpha", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`BEGIN; DELETE FROM documents_search WHERE record_id='a'; DELETE FROM documents WHERE id='a'; COMMIT`); err != nil {
		t.Fatal(err)
	}
	r, err = e.Search(context.Background(), "docs", "alpha", 0)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].ID != "b" {
		t.Fatalf("delete %#v %v", r, err)
	}
}
func TestCancellation(t *testing.T) {
	e, _, _, _ := fixture(t, Limits{Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Search(ctx, "docs", "alpha", 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := e.Search(context.Background(), "docs", "alpha", 0); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalConfig(t *testing.T) {
	base := Index{Table: "search_docs", Collection: "docs", Columns: []string{"body"}, SnippetColumn: "body"}
	for _, mutate := range []func(*Index){func(i *Index) { i.Table = "bad\";DROP" }, func(i *Index) { i.Columns = []string{"record_id"} }, func(i *Index) { i.Columns = []string{"body", "BODY"} }, func(i *Index) { i.SnippetColumn = "secret" }, func(i *Index) { i.Weights = []float64{-1} }, func(i *Index) { i.Table = i.Collection }} {
		i := base
		mutate(&i)
		if _, err := CanonicalDDL(i); err == nil {
			t.Fatalf("accepted %#v", i)
		}
	}
}

func TestReadOnlyPoolAndBoundedResponse(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{MaxBytes: 128})
	for _, q := range []string{`DELETE FROM documents_search`, `SELECT load_extension('bad')`, `SELECT * FROM pragma_table_info('documents')`, `PRAGMA query_only=OFF`, `SELECT title FROM documents`, `ATTACH ':memory:' AS other`} {
		if rows, err := e.db.Query(q); err == nil {
			rows.Close()
			t.Fatalf("permitted unsafe pool statement %s", q)
		}
	}
	if _, err := db.Exec(`UPDATE documents SET body=? WHERE id='a';`, strings.Repeat("alpha ", 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM documents_search WHERE record_id='a'; INSERT INTO documents_search(record_id,title,body) SELECT id,title,body FROM documents WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Search(context.Background(), "docs", "alpha", 0); !errors.Is(err, ErrResponseLimit) {
		t.Fatalf("byte limit: %v", err)
	}
}

func TestRejectOtherFTSShapes(t *testing.T) {
	for _, suffix := range []string{`, content=''`, `, content='documents'`, `, tokenize='porter'`, `, prefix='2 3'`, `, detail=none`} {
		t.Run(suffix, func(t *testing.T) {
			e, db, idx, path := fixture(t, Limits{})
			e.Close()
			ddl := `CREATE VIRTUAL TABLE documents_search USING fts5(record_id UNINDEXED,title,body` + suffix + `)`
			if _, err := db.Exec(`DROP TABLE documents_search;` + ddl); err != nil {
				t.Fatal(err)
			}
			if e, err := New(path, Config{Indexes: map[string]Index{"docs": idx}}, Limits{}); err == nil {
				e.Close()
				t.Fatal("accepted noncanonical DDL")
			}
		})
	}
}

func TestRunningCancellationAndPoolRecovery(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{})
	if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<10000) INSERT INTO documents SELECT 'bulk'||n,'alpha','alpha body' FROM seq; INSERT INTO documents_search(record_id,title,body) SELECT id,title,body FROM documents WHERE id LIKE 'bulk%'`); err != nil {
		t.Fatal(err)
	}
	e.limits.Timeout = time.Millisecond
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Go(func() {
			if _, err := e.Search(context.Background(), "docs", "alpha", 0); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("running deadline: %v", err)
			}
		})
	}
	wg.Wait()
	e.limits.Timeout = 2 * time.Second
	if _, err := e.Search(context.Background(), "docs", "alpha", 1); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}

func TestSchemaCommitDuringSearch(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{})
	e.db.SetMaxOpenConns(1)
	conn, err := e.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	err = conn.Raw(func(raw any) error {
		return raw.(readConn).c.RegisterFunc("pocketcontext_utf8_valid", func(value string) (bool, error) {
			var mutationErr error
			once.Do(func() {
				_, mutationErr = db.Exec(`DROP TABLE documents_search; CREATE TABLE documents_search(record_id,title,body); INSERT INTO documents_search VALUES('private','private','private')`)
			})
			return utf8.ValidString(value), mutationErr
		}, true)
	})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Search(context.Background(), "docs", "alpha", 0)
	if err != nil || len(r.Hits) != 2 {
		t.Fatalf("in-flight snapshot %#v %v", r, err)
	}
	if _, err = e.Search(context.Background(), "docs", "alpha", 0); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("next request accepted schema change: %v", err)
	}
}

func TestNullExcerptAndInvalidIDEncoding(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{})
	if _, err := db.Exec(`INSERT INTO documents VALUES('nullbody','nullable',NULL); INSERT INTO documents_search(record_id,title,body) VALUES('nullbody','nullable',NULL)`); err != nil {
		t.Fatal(err)
	}
	r, err := e.Search(context.Background(), "docs", "nullable", 0)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Excerpt != "" {
		t.Fatalf("null excerpt %#v %v", r, err)
	}
	invalid := string([]byte{255})
	if _, err := db.Exec(`INSERT INTO documents VALUES(?, 'alpha', 'alpha')`, invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO documents_search(record_id,title,body) VALUES(?, 'alpha', 'alpha')`, invalid); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Search(context.Background(), "docs", "alpha", 0); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("invalid UTF8 ID: %v", err)
	}
}

func TestSchemaValidationBusyClassification(t *testing.T) {
	e, db, _, _ := fixture(t, Limits{Timeout: time.Second})
	// Rollback-journal EXCLUSIVE prevents the source-schema validation read.
	// BEGIN itself is deferred, so the failure comes from schema inspection.
	e.db.SetMaxIdleConns(0)
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	e.db.SetMaxIdleConns(4)
	if _, err := e.Search(context.Background(), "docs", "alpha", 0); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), `ROLLBACK`)
	_, err = e.Search(context.Background(), "docs", "alpha", 0)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("schema busy identity not preserved: %v", err)
	}
	if _, err = writer.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Search(context.Background(), "docs", "alpha", 0); err != nil {
		t.Fatalf("after writer released: %v", err)
	}
}

// Built indexes: synthetic revisions and publication manifests. Manifest
// values are revision IDs; r3 is archived.
func builtFixture(t *testing.T, mutate func(*Config)) (*Engine, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "built.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE revisions(id TEXT PRIMARY KEY, page TEXT, title TEXT, body TEXT, archived INTEGER, rank_hint INTEGER, secret TEXT);
CREATE TABLE publications(id TEXT PRIMARY KEY, manifest TEXT);
INSERT INTO revisions VALUES
 ('r1','guide','Rocket guide','synthetic rocket launch checklist',0,1,'rocket secret'),
 ('r2','garden','Garden notes','rocket rocket rocket in the garden',0,2,NULL),
 ('r3','draft','Archived draft','archived rocket draft',1,3,NULL),
 ('r4','null','Null body',NULL,0,4,NULL),
 ('old1','guide','Rocket guide v0','older rocket text',0,5,NULL);
INSERT INTO publications VALUES
 ('p1','{"guide":"r1","garden":"r2","null":"r4"}'),
 ('p2','{"guide":"r1","draft":"r3"}'),
 ('p3','{"guide":"old1"}');`)
	if err != nil {
		t.Fatal(err)
	}
	manifest := &Scope{Collection: "publications", Field: "manifest"}
	cfg := Config{
		Indexes: map[string]Index{
			"pages":   {Collection: "revisions", Columns: []string{"title", "body"}, SnippetColumn: "body", Weights: []float64{8, 1}, Scope: manifest, Members: MembersVisible, Where: "archived = 0"},
			"all":     {Collection: "revisions", Columns: []string{"title", "body"}, SnippetColumn: "body", Scope: manifest},
			"current": {Collection: "revisions", Columns: []string{"title", "body"}, SnippetColumn: "body", Weights: []float64{5, 1}, Where: "archived = 0 AND id <> 'old1'"},
		},
		Readable: map[string][]string{"revisions": {"id", "page", "title", "body", "archived", "rank_hint"}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e, err := New(path, cfg, Limits{Timeout: time.Second, MaxRows: 100, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, db
}

func hitIDs(r Result) string {
	out := []string{}
	for _, h := range r.Hits {
		out = append(out, h.ID)
	}
	return strings.Join(out, ",")
}

func TestBuiltVisibleRevisionScope(t *testing.T) {
	e, db := builtFixture(t, nil)
	ctx := context.Background()
	r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1"})
	if err != nil || hitIDs(r) != "r1,r2" || r.Scope != "p1" || len(r.Generation) != 64 || !strings.Contains(r.Hits[0].Excerpt, "rocket") {
		t.Fatalf("published revisions: %+v %v", r, err)
	}
	// Membership comes from manifest values (revision IDs), not keys (pages).
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p3"}); err != nil || hitIDs(r) != "old1" {
		t.Fatalf("manifest values: %+v %v", r, err)
	}
	// visible drops archived members; all refuses partial results instead.
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p2"}); err != nil || hitIDs(r) != "r1" {
		t.Fatalf("archived excluded: %+v %v", r, err)
	}
	if r, err := e.SearchRequest(ctx, Request{Index: "all", Query: "rocket", Scope: "p2"}); err != nil || hitIDs(r) != "r1,r3" && hitIDs(r) != "r3,r1" {
		t.Fatalf("members all without where: %+v %v", r, err)
	}
	if _, err := db.Exec(`UPDATE publications SET manifest='{"guide":"r1","gone":"missing"}' WHERE id='p2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "all", Query: "rocket", Scope: "p2"}); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("members all with a missing member: %v", err)
	}
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p2"}); err != nil || hitIDs(r) != "r1" {
		t.Fatalf("members visible with a missing member: %+v %v", r, err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "missing"}); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("missing scope: %v", err)
	}
	// Columns outside the index are never searchable.
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "secret", Scope: "p1"}); err != nil || len(r.Hits) != 0 {
		t.Fatalf("unindexed column searched: %+v %v", r, err)
	}
	for _, req := range []Request{
		{Index: "pages", Query: "rocket"},
		{Index: "current", Query: "rocket", Scope: "p1"},
		{Index: "pages", Query: "rocket", Scope: "p1", Offset: 1},
		{Index: "pages", Query: "rocket", Scope: "p1", Offset: MaxOffset + 1, ExpectedGeneration: "x"},
		{Index: "pages", Query: "", Scope: "p1"},
		{Index: "pages", Query: "rocket", Scope: "p1", Limit: 101},
		{Index: "missing", Query: "rocket"},
	} {
		if _, err := e.SearchRequest(ctx, req); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%+v: %v", req, err)
		}
	}
}

func TestBuiltWhereAndInvalidContent(t *testing.T) {
	e, db := builtFixture(t, nil)
	ctx := context.Background()
	if r, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"}); err != nil || hitIDs(r) != "r1,r2" || r.Scope != "" || len(r.Generation) != 64 {
		t.Fatalf("where: %+v %v", r, err)
	}
	for _, manifest := range []string{`[]`, `{"a":"r1","a":"r2"}`, `{"a":1}`, `not json`} {
		if _, err := db.Exec("UPDATE publications SET manifest=? WHERE id='p1'", manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1"}); !errors.Is(err, ErrIndexInvalid) {
			t.Fatalf("%s: %v", manifest, err)
		}
	}
	if _, err := db.Exec("UPDATE publications SET manifest=NULL WHERE id='p1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1"}); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("null membership: %v", err)
	}
	if _, err := db.Exec("UPDATE publications SET manifest='{}' WHERE id='p1'; UPDATE revisions SET body=x'00ff' WHERE id='r1'"); err != nil {
		t.Fatal(err)
	}
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1"}); err != nil || len(r.Hits) != 0 {
		t.Fatalf("empty scope: %+v %v", r, err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"}); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("blob value: %v", err)
	}
	numeric, _ := builtFixture(t, func(c *Config) {
		c.Indexes = map[string]Index{"hints": {Collection: "revisions", Columns: []string{"rank_hint"}, SnippetColumn: "rank_hint"}}
	})
	if _, err := numeric.SearchRequest(ctx, Request{Index: "hints", Query: "1"}); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("integer value: %v", err)
	}
}

func TestBuiltPaginationAndGeneration(t *testing.T) {
	e, db := builtFixture(t, nil)
	ctx := context.Background()
	first, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1", Limit: 1})
	if err != nil || len(first.Hits) != 1 || !first.HasMore || !first.Truncated {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1", Limit: 1, Offset: 1, ExpectedGeneration: first.Generation})
	if err != nil || len(second.Hits) != 1 || second.Hits[0].ID == first.Hits[0].ID || second.Generation != first.Generation || second.HasMore {
		t.Fatalf("second: %+v %v", second, err)
	}
	// Changes outside the scope and to archived rows keep the generation.
	if _, err := db.Exec("UPDATE revisions SET body='changed rocket' WHERE id IN ('r3','old1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1", Limit: 1, Offset: 1, ExpectedGeneration: first.Generation}); err != nil {
		t.Fatalf("unrelated change: %v", err)
	}
	if _, err := db.Exec("UPDATE revisions SET body='changed rocket' WHERE id='r2'"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1", Limit: 1, Offset: 1, ExpectedGeneration: first.Generation}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("indexed change: %v", err)
	}
	// Archiving a member removes it on the next request despite the cache.
	if _, err := db.Exec("UPDATE revisions SET archived=1 WHERE id='r2'"); err != nil {
		t.Fatal(err)
	}
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1"}); err != nil || hitIDs(r) != "r1" {
		t.Fatalf("archived member: %+v %v", r, err)
	}
	// The generation binds ranking configuration as well as content.
	reweighted, _ := builtFixture(t, func(c *Config) {
		idx := c.Indexes["pages"]
		idx.Weights = []float64{1, 8}
		c.Indexes["pages"] = idx
	})
	other, err := reweighted.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "p1", Limit: 1})
	if err != nil || other.Generation == first.Generation {
		t.Fatalf("generation ignores weights: %+v %v", other, err)
	}
}

func TestBuiltIndexByteLimitAndCache(t *testing.T) {
	e, db := builtFixture(t, func(c *Config) { c.MaxIndexBytes, c.CacheBytes = 1024, 100<<10 })
	ctx := context.Background()
	if _, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE revisions SET body=? WHERE id='r2'", strings.Repeat("rocket ", 400)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"}); !errors.Is(err, ErrIndexLimit) {
		t.Fatalf("index limit: %v", err)
	}
	if _, err := e.SearchRequest(ctx, Request{Index: "pages", Scope: "p3", Query: "rocket"}); err != nil {
		t.Fatalf("other index unaffected: %v", err)
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("n%02d", i)
		if _, err := db.Exec("INSERT INTO revisions VALUES(?, 'p', 'note', ?, 0, 0, NULL); INSERT INTO publications VALUES(?, ?)", id, "distinct rocket "+id, "scope"+id, `{"a":"`+id+`"}`); err != nil {
			t.Fatal(err)
		}
		r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "scope" + id})
		if err != nil || hitIDs(r) != id {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
		if entries, bytes := e.cache.stats(); entries < 1 || bytes > 100<<10 || entries > MaxCacheEntries {
			t.Fatalf("cache bounds: %d entries %d bytes", entries, bytes)
		}
	}
	// A failed pinned connection yields an error, never empty results, and is
	// replaced on the next request.
	r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "scopen00"})
	if err != nil {
		t.Fatal(err)
	}
	entry := e.cache.acquire(r.Generation)
	if entry == nil {
		t.Fatal("entry not cached")
	}
	entry.conn.Close()
	e.cache.release(entry)
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "scopen00"}); err == nil {
		t.Fatalf("lost connection returned results: %+v", r)
	}
	if r, err := e.SearchRequest(ctx, Request{Index: "pages", Query: "rocket", Scope: "scopen00"}); err != nil || hitIDs(r) != "n00" {
		t.Fatalf("rebuild: %+v %v", r, err)
	}
	e.Close()
	if entries, bytes := e.cache.stats(); entries != 0 || bytes != 0 {
		t.Fatalf("close: %d %d", entries, bytes)
	}
}

func TestBuiltAdmissionAndCachedQueryCancellation(t *testing.T) {
	e, _ := builtFixture(t, nil)
	for i := 0; i < concurrentBuiltRequests; i++ {
		e.requests <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"})
	cancel()
	for i := 0; i < concurrentBuiltRequests; i++ {
		<-e.requests
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission wait: %v", err)
	}
	if entries, _ := e.cache.stats(); entries != 0 {
		t.Fatal("queued request built an index")
	}
	r, err := e.SearchRequest(context.Background(), Request{Index: "current", Query: "rocket"})
	if err != nil {
		t.Fatal(err)
	}
	entry := e.cache.acquire(r.Generation)
	defer e.cache.release(entry)
	entry.queries <- struct{}{}
	defer func() { <-entry.queries }()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.SearchRequest(ctx, Request{Index: "current", Query: "rocket"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cached query wait: %v", err)
	}
}

func TestBuiltConcurrentSearches(t *testing.T) {
	e, _ := builtFixture(t, nil)
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			req, want := Request{Index: "current", Query: "rocket"}, "r1,r2"
			if i%2 == 1 {
				req, want = Request{Index: "pages", Scope: "p1", Query: "rocket"}, "r1,r2"
			}
			r, err := e.SearchRequest(context.Background(), req)
			if err == nil && hitIDs(r) != want {
				err = fmt.Errorf("%s: %s", req.Index, hitIDs(r))
			}
			errs <- err
		}(i)
	}
	for i := 0; i < 32; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuiltConfigRejected(t *testing.T) {
	base := Index{Collection: "revisions", Columns: []string{"title"}, SnippetColumn: "title"}
	manifest := &Scope{Collection: "publications", Field: "manifest"}
	for name, mutate := range map[string]func(*Index, *Config){
		"generation": func(i *Index, _ *Config) {
			i.Generation = &Generation{Collection: "publications", Record: "p1", Field: "manifest"}
		},
		"members without scope":   func(i *Index, _ *Config) { i.Members = MembersAll },
		"bad members":             func(i *Index, _ *Config) { i.Scope, i.Members = manifest, "some" },
		"blank where":             func(i *Index, _ *Config) { i.Where = " " },
		"statement in where":      func(i *Index, _ *Config) { i.Where = "1); DELETE FROM revisions; --" },
		"escaping where":          func(i *Index, _ *Config) { i.Where = "archived = 0) OR (1 = 1" },
		"comment in where":        func(i *Index, _ *Config) { i.Where = "archived = 0 /* x */" },
		"where binds":             func(i *Index, _ *Config) { i.Where = "title = :other" },
		"where positional":        func(i *Index, _ *Config) { i.Where = "title = ?" },
		"where unreadable column": func(i *Index, _ *Config) { i.Where = "secret IS NULL" },
		"where unknown column":    func(i *Index, _ *Config) { i.Where = "missing = 0" },
		"where quoted identifier": func(i *Index, _ *Config) { i.Where = `"missing" = 'missing'` },
		"where other table":       func(i *Index, _ *Config) { i.Where = "EXISTS (SELECT 1 FROM publications)" },
		"where schema":            func(i *Index, _ *Config) { i.Where = "EXISTS (SELECT 1 FROM sqlite_schema)" },
		"where function":          func(i *Index, _ *Config) { i.Where = "random() > 0" },
		"unknown column":          func(i *Index, _ *Config) { i.Columns, i.SnippetColumn = []string{"missing"}, "missing" },
		"unknown scope":           func(i *Index, _ *Config) { i.Scope = &Scope{Collection: "publications", Field: "missing"} },
		"reserved column":         func(i *Index, _ *Config) { i.Columns, i.SnippetColumn = []string{"rank"}, "rank" },
		"internal table column":   func(i *Index, _ *Config) { i.Columns, i.SnippetColumn = []string{builtTable}, builtTable },
		"snippet":                 func(i *Index, _ *Config) { i.SnippetColumn = "body" },
		"weights":                 func(i *Index, _ *Config) { i.Weights = []float64{1, 2} },
		"persisted with where":    func(i *Index, _ *Config) { i.Table, i.Where = "revisions_search", "archived = 0" },
		"persisted with members":  func(i *Index, _ *Config) { i.Table, i.Members = "revisions_search", MembersVisible },
		"small maxIndexBytes":     func(_ *Index, c *Config) { c.MaxIndexBytes = 10 },
		"large maxIndexBytes":     func(_ *Index, c *Config) { c.MaxIndexBytes = MaxMaxIndexBytes + 1 },
		"cache below index size":  func(_ *Index, c *Config) { c.MaxIndexBytes, c.CacheBytes = 4096, 2048 },
		"large cache":             func(_ *Index, c *Config) { c.CacheBytes = MaxCacheBytes + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			_, db := builtFixture(t, nil)
			var path string
			if err := db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			idx := base
			cfg := Config{Readable: map[string][]string{"revisions": {"id", "title", "body", "archived"}}}
			mutate(&idx, &cfg)
			cfg.Indexes = map[string]Index{"docs": idx}
			if e, err := New(path, cfg, Limits{}); err == nil {
				e.Close()
				t.Fatal("accepted")
			}
		})
	}
	// Persisted-only configurations keep rejecting built-index limits.
	if err := NormalizeConfig(&Config{Indexes: map[string]Index{"docs": {Table: "docs_search", Collection: "docs", Columns: []string{"title"}, SnippetColumn: "title"}}, MaxIndexBytes: 4096}); err == nil {
		t.Fatal("persisted index accepted built limits")
	}
}
