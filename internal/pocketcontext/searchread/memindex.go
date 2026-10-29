package searchread

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Built indexes have no persisted FTS table. Each request reads the indexed
// rows with fixed SQL inside one read transaction of the live database, and the
// engine builds (or reuses) an in-memory FTS5 index keyed by a fingerprint of
// exactly those rows. The fingerprint is also the exposed generation token.

const (
	DefaultMaxIndexBytes = 8 << 20
	MaxMaxIndexBytes     = 64 << 20
	MinMaxIndexBytes     = 1024
	DefaultCacheBytes    = 64 << 20
	MaxCacheBytes        = 1 << 30
	MaxCacheEntries      = 64

	// builtTable is the fixed internal FTS5 table of every in-memory index.
	builtTable = "built_search"
	// Concurrent in-memory index builds; queries on cached indexes do not wait.
	concurrentBuilds = 2
	// Bound requests that retain read rows and evicted-but-active indexes too.
	concurrentBuiltRequests = 4
)

// Bump whenever built-index ranking, tokenization, query or fingerprint
// semantics change. Independent of the persisted rankingContractVersion.
const builtRankingContractVersion = "built-literal-and-bm25-id-v1"

type builtRow struct {
	id     string
	values []string
}

// builtFingerprint identifies exactly the content and configuration from which
// an in-memory index is built. It is the exposed generation token and the cache key.
func builtFingerprint(idx Index, scope string, rows []builtRow) string {
	version, number, source := sqlite3.Version()
	var scopeMarker any // JSON null marks a scope-less index.
	if idx.Scope != nil {
		scopeMarker = scope
	}
	h := sha256.New()
	header, _ := json.Marshal([]any{builtRankingContractVersion, version, number, source, idx, scopeMarker, len(rows)})
	h.Write(header)
	for _, row := range rows {
		values := make([]any, 0, len(row.values)+1)
		values = append(values, row.id)
		for _, v := range row.values {
			values = append(values, v)
		}
		encoded, _ := json.Marshal(values)
		h.Write(encoded)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (e *Engine) build(ctx context.Context, d definition, rows []builtRow) (*ftsEntry, error) {
	select {
	case e.builds <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-e.builds }()
	raw, err := (&sqlite3.SQLiteDriver{}).Open(":memory:")
	if err != nil {
		return nil, err
	}
	conn := raw.(*sqlite3.SQLiteConn)
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	conn.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, e.maxIndexBytes+e.limits.MaxBytes+65536)
	conn.SetLimit(sqlite3.SQLITE_LIMIT_SQL_LENGTH, 65536)
	conn.SetLimit(sqlite3.SQLITE_LIMIT_COLUMN, 256)
	conn.SetLimit(sqlite3.SQLITE_LIMIT_EXPR_DEPTH, 100)
	conn.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
	// Content and index together stay well below four times the indexed text.
	pages := e.maxIndexBytes*4/4096 + 256
	fail := func(err error) (*ftsEntry, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var se sqlite3.Error
		if errors.As(err, &se) && (se.Code == sqlite3.ErrFull || se.Code == sqlite3.ErrTooBig) {
			return nil, fmt.Errorf("%w: in-memory index size", ErrIndexLimit)
		}
		return nil, classify(err)
	}
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA page_size=4096; PRAGMA max_page_count=%d; PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; PRAGMA temp_store=MEMORY", pages), nil); err != nil {
		return fail(err)
	}
	if _, err = conn.ExecContext(ctx, d.memDDL, nil); err != nil {
		return fail(err)
	}
	if _, err = conn.ExecContext(ctx, "BEGIN", nil); err != nil {
		return fail(err)
	}
	stmt, err := conn.Prepare(d.memInsert)
	if err != nil {
		return fail(err)
	}
	insert := stmt.(driver.StmtExecContext)
	args := make([]driver.NamedValue, len(d.index.Columns)+1)
	for _, row := range rows {
		args[0] = driver.NamedValue{Ordinal: 1, Value: row.id}
		for i, v := range row.values {
			args[i+1] = driver.NamedValue{Ordinal: i + 2, Value: v}
		}
		if _, err = insert.ExecContext(ctx, args); err != nil {
			stmt.Close()
			return fail(err)
		}
	}
	stmt.Close()
	if _, err = conn.ExecContext(ctx, "COMMIT", nil); err != nil {
		return fail(err)
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO "+quote(builtTable)+"("+quote(builtTable)+") VALUES('optimize')", nil); err != nil {
		return fail(err)
	}
	pageCount, err := queryInt(ctx, conn, "PRAGMA page_count")
	if err != nil {
		return fail(err)
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA query_only=1", nil); err != nil {
		return fail(err)
	}
	conn.RegisterAuthorizer(func(op int, a, b, db string) int {
		switch op {
		case sqlite3.SQLITE_SELECT:
			return sqlite3.SQLITE_OK
		case sqlite3.SQLITE_READ:
			if db == "main" || db == "" {
				return sqlite3.SQLITE_OK
			}
		case sqlite3.SQLITE_FUNCTION:
			switch strings.ToLower(b) {
			case "match", "bm25", "snippet":
				return sqlite3.SQLITE_OK
			}
		case sqlite3.SQLITE_PRAGMA:
			// FTS5 checks for concurrent changes with data_version.
			if strings.EqualFold(a, "data_version") && b == "" && db == "main" {
				return sqlite3.SQLITE_OK
			}
		}
		return sqlite3.SQLITE_DENY
	})
	size := 0
	for _, row := range rows {
		size += len(row.id)
		for _, v := range row.values {
			size += len(v)
		}
	}
	success = true
	return &ftsEntry{conn: conn, size: max(size, int(pageCount)*4096), queries: make(chan struct{}, 1)}, nil
}

func queryInt(ctx context.Context, conn *sqlite3.SQLiteConn, query string) (int64, error) {
	rows, err := conn.QueryContext(ctx, query, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	dest := make([]driver.Value, 1)
	if err = rows.Next(dest); err != nil {
		return 0, err
	}
	n, ok := dest[0].(int64)
	if !ok {
		return 0, ErrIndexInvalid
	}
	return n, nil
}

func (e *Engine) queryBuilt(ctx context.Context, entry *ftsEntry, d definition, match string, limit, offset int) ([]Hit, bool, error) {
	select {
	case entry.queries <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-entry.queries }()
	if entry.conn == nil {
		return nil, false, fmt.Errorf("%w: in-memory index connection lost", ErrIndexInvalid)
	}
	args := []driver.NamedValue{{Ordinal: 1, Value: match}, {Ordinal: 2, Value: int64(limit + 1)}, {Ordinal: 3, Value: int64(offset)}}
	rows, err := entry.conn.QueryContext(ctx, d.memSearch, args)
	if err != nil {
		return nil, false, classify(err)
	}
	defer rows.Close()
	hits := []Hit{}
	dest := make([]driver.Value, 3)
	for {
		err := rows.Next(dest)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, classify(err)
		}
		id, ok := dest[0].(string)
		score, scored := dest[1].(float64)
		if !ok || !scored || !utf8.ValidString(id) || math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, false, ErrIndexInvalid
		}
		hit := Hit{ID: id, Score: score}
		if len(hits) == limit {
			return hits, true, nil
		}
		switch x := dest[2].(type) {
		case nil:
		case string:
			hit.Excerpt = x
		default:
			return nil, false, ErrIndexInvalid
		}
		hits = append(hits, hit)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return hits, false, nil
}

// ftsEntry is one pinned in-memory index connection. Queries serialize on queries.
// The connection closes only after eviction and the last release.
type ftsEntry struct {
	queries chan struct{}
	conn    *sqlite3.SQLiteConn
	key     string
	size    int
	refs    int
	evicted bool
	elem    *list.Element
}

// ftsCache is an LRU of private indexes bounded by bytes and entry count.
type ftsCache struct {
	mu                   sync.Mutex
	maxBytes, maxEntries int
	bytes                int
	lru                  *list.List
	entries              map[string]*ftsEntry
	closed               bool
}

func newFTSCache(maxBytes, maxEntries int) *ftsCache {
	return &ftsCache{maxBytes: maxBytes, maxEntries: maxEntries, lru: list.New(), entries: map[string]*ftsEntry{}}
}

func (c *ftsCache) acquire(key string) *ftsEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	entry.refs++
	c.lru.MoveToFront(entry.elem)
	return entry
}

// insert caches a newly built entry and returns an acquired entry for key.
// Entries larger than the whole cache are used once, then closed.
func (c *ftsCache) insert(key string, built *ftsEntry) *ftsEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	built.key = key
	built.refs = 1
	if existing := c.entries[key]; existing != nil {
		existing.refs++
		c.lru.MoveToFront(existing.elem)
		built.evicted = true
		built.refs = 0
		closeEntry(built)
		return existing
	}
	if c.closed || built.size > c.maxBytes {
		built.evicted = true
		return built
	}
	built.elem = c.lru.PushFront(built)
	c.entries[key] = built
	c.bytes += built.size
	for c.bytes > c.maxBytes || len(c.entries) > c.maxEntries {
		c.evictLocked(c.lru.Back().Value.(*ftsEntry))
	}
	return built
}

func (c *ftsCache) evictLocked(entry *ftsEntry) {
	if entry.evicted {
		return
	}
	entry.evicted = true
	if entry.elem != nil {
		c.lru.Remove(entry.elem)
		entry.elem = nil
	}
	delete(c.entries, entry.key)
	c.bytes -= entry.size
	if entry.refs == 0 {
		closeEntry(entry)
	}
}

func (c *ftsCache) release(entry *ftsEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.refs--
	if entry.evicted && entry.refs == 0 {
		closeEntry(entry)
	}
}

func (c *ftsCache) remove(entry *ftsEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(entry)
}

func (c *ftsCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, entry := range c.entries {
		c.evictLocked(entry)
	}
}

// stats reports cached entries and charged bytes.
func (c *ftsCache) stats() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}

func closeEntry(entry *ftsEntry) {
	// The cache lock is held and no requests retain this entry.
	if entry.conn != nil {
		entry.conn.Close()
		entry.conn = nil
	}
}
