// Package searchread exposes bounded full-text search using fixed SQL only.
package searchread

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/sqliteuri"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/tracing"
)

const (
	MaxQueryBytes   = 4096
	MaxTerms        = 16
	MaxLimit        = 100
	DefaultLimit    = 20
	MaxScopeMembers = 10000
	MaxOffset       = 10000
	MaxScopeBytes   = 4 << 20
)

var (
	ErrGenerationConflict = errors.New("search generation changed")
	ErrInvalidQuery       = errors.New("invalid search request")
	ErrIndexInvalid       = errors.New("search index is invalid")
	ErrBusy               = errors.New("search database busy")
	ErrResponseLimit      = errors.New("search response exceeds byte limit")
	ErrScopeNotFound      = errors.New("search scope not found")
	ErrIndexLimit         = errors.New("authorized search content exceeds configured limits")
)

type Config struct {
	Indexes map[string]Index `json:"indexes"`
	// MaxIndexBytes and CacheBytes bound built (in-memory) indexes only.
	MaxIndexBytes int `json:"maxIndexBytes,omitempty"`
	CacheBytes    int `json:"cacheBytes,omitempty"`
	// Readable lists, per table, the columns that a built index's Where may
	// read in addition to its id and indexed columns. Servers set it to their
	// SQL column allowlist; it is never read from configuration files.
	Readable map[string][]string `json:"-"`
}
type Scope struct {
	Collection string `json:"collection"`
	Field      string `json:"field"`
}
type Generation struct {
	Collection string `json:"collection"`
	Record     string `json:"record"`
	Field      string `json:"field"`
}
type Request struct {
	Index              string `json:"index"`
	Query              string `json:"query"`
	Limit              int    `json:"limit"`
	Scope              string `json:"scope,omitempty"`
	Offset             int    `json:"offset,omitempty"`
	ExpectedGeneration string `json:"expectedGeneration,omitempty"`
}

// Index is either persisted or built. A persisted index (Table set) is an FTS5
// table that trusted application code maintains; scoped persisted indexes
// also name a Generation record. A built index (no Table) has no stored FTS
// data: each request reads its rows with fixed SQL from the live database and
// searches an in-memory index (see memindex.go). Members and Where apply only
// to built indexes; both are omitted for persisted indexes, keeping their
// generation tokens.
type Index struct {
	Scope         *Scope      `json:"scope,omitempty"`
	Generation    *Generation `json:"generation,omitempty"`
	Table         string      `json:"table"`
	Collection    string      `json:"collection"`
	Columns       []string    `json:"columns"`
	SnippetColumn string      `json:"snippetColumn"`
	Weights       []float64   `json:"weights,omitempty"`
	// Members selects how scope members (the values of the scope field's JSON
	// object) are treated: "all" (default) requires every member row to exist
	// and pass Where; "visible" searches only those that do.
	Members string `json:"members,omitempty"`
	// Where is a trusted SQL predicate over the source collection's own
	// columns, such as "archived = 0". It binds no parameters.
	Where string `json:"where,omitempty"`
}

const (
	MembersAll     = "all"
	MembersVisible = "visible"
)

// Built reports whether an index is built in memory per request.
func Built(i Index) bool { return i.Table == "" }

type Limits struct {
	Timeout           time.Duration
	MaxRows, MaxBytes int
}
type Hit struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Excerpt string  `json:"excerpt"`
}
type Result struct {
	Scope      string `json:"scope,omitempty"`
	Generation string `json:"generation,omitempty"`
	HasMore    bool   `json:"hasMore"`
	Index      string `json:"index"`
	Hits       []Hit  `json:"hits"`
	Truncated  bool   `json:"truncated"`
}
type definition struct {
	index            Index
	schema           map[string]string
	search, validIDs string
	// Built indexes: the tables they read, their fixed reads of the scope
	// record and of the indexed rows, and their in-memory FTS statements.
	sources                      []string
	scopeRead, rowsRead          string
	memDDL, memInsert, memSearch string
}
type Engine struct {
	db               *sql.DB
	indexes          map[string]definition
	limits           Limits
	maxIndexBytes    int
	cache            *ftsCache
	builds, requests chan struct{}
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func validName(s string) bool {
	return identifier.MatchString(s) && !strings.HasPrefix(strings.ToLower(s), "sqlite_")
}
func quote(s string) string { return `"` + s + `"` }

// CanonicalDDL returns the only accepted index shape. Applications execute this
// DDL through trusted migrations, then maintain index rows transactionally.
func CanonicalDDL(i Index) (string, error) {
	if (i.Scope == nil) != (i.Generation == nil) {
		return "", ErrIndexInvalid
	}
	if i.Scope != nil && (!validName(i.Scope.Collection) || !validName(i.Scope.Field) || !validName(i.Generation.Collection) || !validName(i.Generation.Field) || !validToken(i.Generation.Record)) {
		return "", ErrIndexInvalid
	}
	if !validName(i.Table) || !validName(i.Collection) || strings.EqualFold(i.Table, i.Collection) || len(i.Columns) == 0 || len(i.Columns) > 16 {
		return "", fmt.Errorf("%w: index identifiers or column count", ErrIndexInvalid)
	}
	seen := map[string]bool{"record_id": true}
	cols := []string{`"record_id" UNINDEXED`}
	snippet := false
	for _, c := range i.Columns {
		if !validName(c) || seen[strings.ToLower(c)] {
			return "", fmt.Errorf("%w: column names", ErrIndexInvalid)
		}
		seen[strings.ToLower(c)] = true
		cols = append(cols, quote(c))
		if c == i.SnippetColumn {
			snippet = true
		}
	}
	if !snippet {
		return "", fmt.Errorf("%w: snippet column", ErrIndexInvalid)
	}
	if len(i.Weights) != 0 && len(i.Weights) != len(i.Columns) {
		return "", fmt.Errorf("%w: weights", ErrIndexInvalid)
	}
	for _, w := range i.Weights {
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 || w > 100 {
			return "", fmt.Errorf("%w: weight range", ErrIndexInvalid)
		}
	}
	return "CREATE VIRTUAL TABLE " + quote(i.Table) + " USING fts5(" + strings.Join(cols, ", ") + ", tokenize='unicode61')", nil
}

// connector only exposes read-only prepared statements to database/sql.
type connector struct {
	dsn      string
	allow    func(int, string, string, string) int
	maxBytes int
}

func (c connector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }
func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	d := sqlite3.SQLiteDriver{}
	raw, err := d.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	db := raw.(*sqlite3.SQLiteConn)
	fail := func(err error) (driver.Conn, error) { db.Close(); return nil, err }
	if _, err = db.Exec("PRAGMA query_only=1; PRAGMA hard_heap_limit=268435456", nil); err != nil {
		return fail(err)
	}
	// Scope documents have a separate bounded allowance even with tiny response caps.
	db.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, max(c.maxBytes, MaxScopeBytes))
	db.SetLimit(sqlite3.SQLITE_LIMIT_SQL_LENGTH, 65536)
	db.SetLimit(sqlite3.SQLITE_LIMIT_COLUMN, 256)
	db.SetLimit(sqlite3.SQLITE_LIMIT_EXPR_DEPTH, 100)
	db.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
	if err = db.RegisterFunc("pocketcontext_utf8_valid", utf8.ValidString, true); err != nil {
		return fail(err)
	}
	db.RegisterAuthorizer(c.allow)
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	return readConn{db}, nil
}

type readConn struct{ c *sqlite3.SQLiteConn }

func (r readConn) Close() error { return r.c.Close() }
func (r readConn) Begin() (driver.Tx, error) {
	tx, err := r.c.Begin()
	if err != nil {
		return nil, err
	}
	return readTx{tx}, nil
}

type readTx struct{ driver.Tx }

func (t readTx) Rollback() error {
	if err := t.Tx.Rollback(); err != nil {
		return driver.ErrBadConn
	}
	return nil
}
func (t readTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return driver.ErrBadConn
	}
	return nil
}
func (r readConn) Prepare(s string) (driver.Stmt, error) {
	return r.PrepareContext(context.Background(), s)
}
func (r readConn) PrepareContext(ctx context.Context, s string) (driver.Stmt, error) {
	stmt, err := r.c.PrepareContext(ctx, s)
	if err != nil {
		return nil, err
	}
	if x, ok := stmt.(*sqlite3.SQLiteStmt); !ok || !x.Readonly() {
		stmt.Close()
		return nil, errors.New("search statement is not read-only")
	}
	return stmt, nil
}
func classify(err error) error {
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code == sqlite3.ErrTooBig {
		return fmt.Errorf("%w: SQLite value limit", ErrResponseLimit)
	}
	if errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked) {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}

func New(path string, cfg Config, limits Limits) (*Engine, error) {
	if len(cfg.Indexes) == 0 || len(cfg.Indexes) > 16 {
		return nil, fmt.Errorf("%w: index count", ErrIndexInvalid)
	}
	if limits.Timeout <= 0 {
		limits.Timeout = 2 * time.Second
	}
	if limits.MaxRows <= 0 || limits.MaxRows > MaxLimit {
		limits.MaxRows = MaxLimit
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 1 << 20
	}
	if limits.MaxBytes < 128 {
		return nil, ErrResponseLimit
	}
	if err := NormalizeConfig(&cfg); err != nil {
		return nil, err
	}
	e := &Engine{indexes: map[string]definition{}, limits: limits, maxIndexBytes: cfg.MaxIndexBytes, cache: newFTSCache(cfg.CacheBytes, MaxCacheEntries), builds: make(chan struct{}, concurrentBuilds), requests: make(chan struct{}, concurrentBuiltRequests)}
	// Generate expected shadow DDL with the pinned SQLite implementation rather
	// than trusting metadata supplied by the application's database.
	expected, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	allowed := map[string]map[string]bool{"sqlite_master": {"type": true, "name": true, "sql": true}, "sqlite_schema": {"type": true, "name": true, "sql": true}}
	allowed["json_each"] = map[string]bool{"value": true, "json": true}
	tableNames := map[string]bool{}
	aliases := make([]string, 0, len(cfg.Indexes))
	for alias := range cfg.Indexes {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		idx := cfg.Indexes[alias]
		if idx.Scope != nil {
			copied := *idx.Scope
			idx.Scope = &copied
		}
		if idx.Generation != nil {
			copied := *idx.Generation
			idx.Generation = &copied
		}
		idx.Columns = append([]string(nil), idx.Columns...)
		idx.Weights = append([]float64(nil), idx.Weights...)
		if !validName(alias) {
			return nil, fmt.Errorf("%w: duplicate index or alias", ErrIndexInvalid)
		}
		if Built(idx) {
			def, err := prepareBuilt(expected, idx, cfg.Readable, allowed)
			if err != nil {
				return nil, fmt.Errorf("search index %q: %w", alias, err)
			}
			e.indexes[alias] = def
			continue
		}
		ddl, err := CanonicalDDL(idx)
		if err != nil {
			return nil, err
		}
		if !validName(alias) || tableNames[strings.ToLower(idx.Table)] {
			return nil, fmt.Errorf("%w: duplicate index or alias", ErrIndexInvalid)
		}
		tableNames[strings.ToLower(idx.Table)] = true
		if _, err = expected.Exec(ddl); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIndexInvalid, err)
		}
		names := []string{idx.Table, idx.Table + "_data", idx.Table + "_idx", idx.Table + "_content", idx.Table + "_docsize", idx.Table + "_config"}
		schema := map[string]string{}
		for _, name := range names {
			var create string
			if err = expected.QueryRow("SELECT sql FROM sqlite_schema WHERE name=? AND type='table'", name).Scan(&create); err != nil {
				return nil, err
			}
			schema[name] = create
			cols, err := expected.Query("PRAGMA table_info(" + quote(name) + ")")
			if err != nil {
				return nil, err
			}
			colset := map[string]bool{"": true}
			for cols.Next() {
				var cid, nn, pk int
				var col, typ string
				var def any
				if err = cols.Scan(&cid, &col, &typ, &nn, &def, &pk); err != nil {
					cols.Close()
					return nil, err
				}
				colset[strings.ToLower(col)] = true
			}
			err = cols.Err()
			cols.Close()
			if err != nil {
				return nil, err
			}
			allowed[strings.ToLower(name)] = colset
		}
		allowed[strings.ToLower(idx.Table)][strings.ToLower(idx.Table)] = true // FTS hidden table argument.
		allowed[strings.ToLower(idx.Table)]["rowid"] = true
		source := strings.ToLower(idx.Collection)
		if allowed[source] == nil {
			allowed[source] = map[string]bool{}
		}
		allowed[source]["id"] = true
		if idx.Scope != nil {
			for _, pair := range [][2]string{{idx.Scope.Collection, idx.Scope.Field}, {idx.Generation.Collection, idx.Generation.Field}} {
				name := strings.ToLower(pair[0])
				if allowed[name] == nil {
					allowed[name] = map[string]bool{}
				}
				allowed[name]["id"] = true
				allowed[name][strings.ToLower(pair[1])] = true
			}
		}
		weights := []string{"0"}
		snip := 0
		for n, c := range idx.Columns {
			w := 1.0
			if len(idx.Weights) > 0 {
				w = idx.Weights[n]
			}
			weights = append(weights, fmt.Sprintf("%g", w))
			if c == idx.SnippetColumn {
				snip = n + 1
			}
		}
		table := quote(idx.Table)
		search := "SELECT record_id, bm25(" + table + "," + strings.Join(weights, ",") + "), snippet(" + table + fmt.Sprintf(",%d,'','',' … ',32) FROM ", snip) + table + " WHERE " + table + " MATCH ? ORDER BY 2, record_id LIMIT ?"
		if idx.Scope != nil {
			search = strings.Replace(search, " ORDER BY", " AND record_id IN (SELECT value FROM json_each(?)) ORDER BY", 1) + " OFFSET ?"
		}
		// Canonical FTS DDL and exact shadow DDL establish that _content.c0
		// stores record_id. Validate the same corpus directly, avoiding FTS's
		// per-row content lookup during an otherwise unindexed virtual-table scan.
		// These fixed internal reads never enter the arbitrary SQL reader.
		content := quote(idx.Table + "_content")
		validIDs := "SELECT 1 FROM " + content + " WHERE typeof(c0) != 'text' OR length(trim(c0))=0 OR NOT pocketcontext_utf8_valid(c0) OR length(CAST(c0 AS BLOB))>128 OR NOT EXISTS (SELECT 1 FROM " + quote(idx.Collection) + " WHERE id=" + content + ".c0) UNION ALL SELECT 1 FROM " + content + " GROUP BY c0 HAVING count(*)>1 LIMIT 1"
		e.indexes[alias] = definition{index: idx, schema: schema, search: search, validIDs: validIDs}
	}
	// Prevent one configured index/source name from impersonating another shadow.
	for _, def := range e.indexes {
		for _, other := range e.indexes {
			for name := range def.schema {
				if strings.EqualFold(name, other.index.Collection) || (other.index.Scope != nil && (strings.EqualFold(name, other.index.Scope.Collection) || strings.EqualFold(name, other.index.Generation.Collection))) {
					return nil, fmt.Errorf("%w: source/index name collision", ErrIndexInvalid)
				}
			}
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := sqliteuri.File(abs)
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_busy_timeout", "100")
	u.RawQuery = q.Encode()
	allow := func(op int, a, b, db string) int {
		switch op {
		case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_TRANSACTION:
			return sqlite3.SQLITE_OK
		case sqlite3.SQLITE_READ:
			if (db == "main" || (db == "" && (b == "" || strings.EqualFold(a, "json_each")))) && allowed[strings.ToLower(a)][strings.ToLower(b)] {
				return sqlite3.SQLITE_OK
			}
		case sqlite3.SQLITE_FUNCTION:
			switch strings.ToLower(b) {
			case "match", "bm25", "snippet", "typeof", "length", "trim", "count", "pocketcontext_utf8_valid", "lower":
				return sqlite3.SQLITE_OK
			}
		case sqlite3.SQLITE_PRAGMA:
			if strings.EqualFold(a, "data_version") && b == "" && db == "main" {
				return sqlite3.SQLITE_OK
			}
		}
		return sqlite3.SQLITE_DENY
	}
	e.db = sql.OpenDB(connector{dsn: u.String(), allow: allow, maxBytes: limits.MaxBytes})
	e.db.SetMaxOpenConns(4)
	e.db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), limits.Timeout)
	defer cancel()
	for _, alias := range aliases {
		if err := e.checkBuiltReads(ctx, e.indexes[alias]); err != nil {
			e.Close()
			return nil, fmt.Errorf("%w: search index %q: %v", ErrIndexInvalid, alias, err)
		}
		tx, err := e.db.BeginTx(ctx, nil)
		if err == nil {
			err = e.validate(ctx, tx, e.indexes[alias])
			if rollback := tx.Rollback(); err == nil {
				err = rollback
			}
		}
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("%w: %v", ErrIndexInvalid, err)
		}
	}
	return e, nil
}
func (e *Engine) Close() error {
	e.cache.close()
	return e.db.Close()
}
func (e *Engine) validate(ctx context.Context, tx *sql.Tx, d definition) error {
	var reserved int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE lower(name)='json_each'").Scan(&reserved); err != nil {
		return fmt.Errorf("%w: schema unavailable: %w", ErrIndexInvalid, err)
	}
	if reserved != 0 {
		return ErrIndexInvalid
	}
	sources := d.sources
	if !Built(d.index) {
		sources = []string{d.index.Collection}
		if d.index.Scope != nil {
			sources = append(sources, d.index.Scope.Collection, d.index.Generation.Collection)
		}
	}
	for _, source := range sources {
		var sourceDDL string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name=?", source).Scan(&sourceDDL); err != nil {
			return fmt.Errorf("%w: source schema unavailable: %w", ErrIndexInvalid, err)
		}
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sourceDDL)), "CREATE TABLE") {
			return fmt.Errorf("%w: source must be an ordinary table", ErrIndexInvalid)
		}
	}
	names := make([]string, 0, len(d.schema))
	for name := range d.schema {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var ddl string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name=?", name).Scan(&ddl); err != nil {
			return fmt.Errorf("%w: schema unavailable: %w", ErrIndexInvalid, err)
		}
		if ddl != d.schema[name] {
			return fmt.Errorf("%w: schema changed", ErrIndexInvalid)
		}
	}
	if Built(d.index) {
		return nil
	}
	var found int
	err := tx.QueryRowContext(ctx, d.validIDs).Scan(&found)
	if err == nil {
		return fmt.Errorf("%w: record IDs must be unique nonempty text", ErrIndexInvalid)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}
func (e *Engine) Search(ctx context.Context, index, query string, limit int) (Result, error) {
	return e.SearchRequest(ctx, Request{Index: index, Query: query, Limit: limit})
}
func (e *Engine) SearchRequest(ctx context.Context, req Request) (Result, error) {
	index, query, limit := req.Index, req.Query, req.Limit
	result := Result{Index: index, Hits: []Hit{}}
	d, ok := e.indexes[index]
	if !ok {
		return result, fmt.Errorf("%w: unknown index", ErrInvalidQuery)
	}
	if Built(d.index) {
		return e.searchBuilt(ctx, d, req)
	}
	if d.index.Scope == nil {
		if req.Scope != "" || req.Offset != 0 || req.ExpectedGeneration != "" {
			return result, ErrInvalidQuery
		}
	} else if !validToken(req.Scope) || req.Offset < 0 || req.Offset > MaxOffset || (req.Offset > 0 && req.ExpectedGeneration == "") || (req.ExpectedGeneration != "" && !validToken(req.ExpectedGeneration)) {
		return result, ErrInvalidQuery
	}
	match, err := matchExpression(query)
	if err != nil {
		return result, err
	}
	if limit == 0 {
		limit = min(DefaultLimit, e.limits.MaxRows)
	}
	if limit < 1 || limit > e.limits.MaxRows {
		return result, ErrInvalidQuery
	}
	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return result, classify(err)
	}
	defer tx.Rollback()
	if err = e.validate(ctx, tx, d); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, classify(err)
	}
	args := []any{match}
	if d.index.Scope != nil {
		membership, generation, err := readScope(ctx, tx, d.index, req.Scope)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, classify(err)
		}
		generation = exposedGeneration(d.index, generation, req.Scope, membership)
		if req.ExpectedGeneration != "" && req.ExpectedGeneration != generation {
			return result, ErrGenerationConflict
		}
		result.Scope, result.Generation = req.Scope, generation
		args = append(args, membership, limit+1, req.Offset)
	} else {
		args = append(args, limit+1)
	}
	rows, err := tx.QueryContext(ctx, d.search, args...)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.Hits) == limit {
			result.Truncated = true
			result.HasMore = true
			break
		}
		var hit Hit
		var excerpt sql.NullString
		if err = rows.Scan(&hit.ID, &hit.Score, &excerpt); err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, classify(err)
		}
		hit.Excerpt = excerpt.String
		if !utf8.ValidString(hit.ID) || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
			return Result{}, ErrIndexInvalid
		}
		result.Hits = append(result.Hits, hit)
		encoded, err := json.Marshal(result)
		if err != nil {
			return Result{}, err
		}
		if len(encoded) > e.limits.MaxBytes {
			return Result{}, ErrResponseLimit
		}
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err = rows.Err(); err != nil {
		return Result{}, classify(err)
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > e.limits.MaxBytes {
		return Result{}, ErrResponseLimit
	}
	return result, nil
}

func validToken(s string) bool {
	return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0)
}

// Decode token-by-token so duplicate JSON object keys cannot disappear silently.
func scopeMembers(raw string) ([]string, error) {
	if len(raw) > MaxScopeBytes || !utf8.ValidString(raw) || !validJSONEscapes(raw) {
		return nil, ErrIndexInvalid
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrIndexInvalid
	}
	keys := map[string]bool{}
	values := []string{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, ErrIndexInvalid
		}
		k, ok := key.(string)
		if !ok || !validToken(k) || keys[k] || len(keys) >= MaxScopeMembers {
			return nil, ErrIndexInvalid
		}
		keys[k] = true
		var v string
		if err := dec.Decode(&v); err != nil || !validToken(v) {
			return nil, ErrIndexInvalid
		}
		values = append(values, v)
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return nil, ErrIndexInvalid
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, ErrIndexInvalid
	}
	return values, nil
}
func readScope(ctx context.Context, tx *sql.Tx, idx Index, scope string) (string, string, error) {
	var raw, generation, generationType string
	if err := tx.QueryRowContext(ctx, "SELECT "+quote(idx.Scope.Field)+" FROM "+quote(idx.Scope.Collection)+" WHERE id=?", scope).Scan(&raw); err != nil {
		return "", "", fmt.Errorf("%w: scope unavailable: %w", ErrIndexInvalid, err)
	}
	values, err := scopeMembers(raw)
	if err != nil {
		return "", "", err
	}
	if err = tx.QueryRowContext(ctx, "SELECT "+quote(idx.Generation.Field)+", typeof("+quote(idx.Generation.Field)+") FROM "+quote(idx.Generation.Collection)+" WHERE id=?", idx.Generation.Record).Scan(&generation, &generationType); err != nil {
		return "", "", fmt.Errorf("%w: generation unavailable: %w", ErrIndexInvalid, err)
	}
	if generationType != "text" || !validToken(generation) {
		return "", "", ErrIndexInvalid
	}
	sort.Strings(values)
	data, _ := json.Marshal(values)
	var missing int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM json_each(?) WHERE NOT EXISTS (SELECT 1 FROM "+quote(idx.Collection)+" WHERE id=json_each.value)", string(data)).Scan(&missing)
	if err != nil {
		return "", "", err
	}
	if missing != 0 {
		return "", "", ErrIndexInvalid
	}
	return string(data), generation, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject them
// instead: corrupted membership must not select an actual replacement-character ID.
func validJSONEscapes(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1:i+3] != `\u` {
				return false
			}
			low, err := strconv.ParseUint(raw[i+3:i+7], 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		} else if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
	}
	return true
}

// Bump this contract version whenever ranking or query semantics change. The
// token is a consistency fingerprint, not an authorization credential or MAC.
const rankingContractVersion = "literal-and-bm25-id-v1"

func exposedGeneration(idx Index, raw, scope, membership string) string {
	version, number, source := sqlite3.Version()
	data, _ := json.Marshal([]any{rankingContractVersion, version, number, source, idx, raw, scope, membership})
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func matchExpression(query string) (string, error) {
	if len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", ErrInvalidQuery
	}
	terms := strings.Fields(query)
	if len(terms) == 0 || len(terms) > MaxTerms {
		return "", ErrInvalidQuery
	}
	for n, term := range terms {
		terms[n] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(terms, " AND "), nil
}

// NormalizeConfig validates index shapes and applies the built-index size
// defaults. Size limits are rejected when no index is built.
func NormalizeConfig(c *Config) error {
	if c == nil || len(c.Indexes) == 0 || len(c.Indexes) > 16 {
		return fmt.Errorf("%w: index count", ErrIndexInvalid)
	}
	built := false
	for alias, idx := range c.Indexes {
		if !validName(alias) {
			return fmt.Errorf("%w: index alias %q", ErrIndexInvalid, alias)
		}
		if Built(idx) {
			built = true
			if err := validateBuilt(idx); err != nil {
				return fmt.Errorf("search index %q: %w", alias, err)
			}
		} else if idx.Members != "" || idx.Where != "" {
			return fmt.Errorf("search index %q: %w: members and where require a built index without table", alias, ErrIndexInvalid)
		}
	}
	if !built {
		if c.MaxIndexBytes != 0 || c.CacheBytes != 0 {
			return fmt.Errorf("%w: maxIndexBytes and cacheBytes apply only to built indexes", ErrIndexInvalid)
		}
		return nil
	}
	if c.MaxIndexBytes == 0 {
		c.MaxIndexBytes = DefaultMaxIndexBytes
	}
	if c.CacheBytes == 0 {
		c.CacheBytes = DefaultCacheBytes
	}
	if c.MaxIndexBytes < MinMaxIndexBytes || c.MaxIndexBytes > MaxMaxIndexBytes {
		return fmt.Errorf("%w: maxIndexBytes must be %d..%d", ErrIndexInvalid, MinMaxIndexBytes, MaxMaxIndexBytes)
	}
	if c.CacheBytes < c.MaxIndexBytes || c.CacheBytes > MaxCacheBytes {
		return fmt.Errorf("%w: cacheBytes must be maxIndexBytes..%d", ErrIndexInvalid, MaxCacheBytes)
	}
	return nil
}

func validateBuilt(i Index) error {
	if i.Generation != nil {
		return fmt.Errorf("%w: a built index has no generation record", ErrIndexInvalid)
	}
	if i.Scope == nil {
		if i.Members != "" {
			return fmt.Errorf("%w: members requires scope", ErrIndexInvalid)
		}
	} else {
		if !validName(i.Scope.Collection) || !validName(i.Scope.Field) {
			return fmt.Errorf("%w: scope identifiers", ErrIndexInvalid)
		}
		if i.Members != "" && i.Members != MembersAll && i.Members != MembersVisible {
			return fmt.Errorf("%w: members must be \"all\" or \"visible\"", ErrIndexInvalid)
		}
	}
	if i.Where != "" {
		if err := validWhere(i.Where); err != nil {
			return err
		}
	}
	for _, c := range i.Columns {
		if strings.EqualFold(c, builtTable) || strings.EqualFold(c, "id") {
			return fmt.Errorf("%w: column names", ErrIndexInvalid)
		}
	}
	_, err := CanonicalDDL(builtShape(i))
	return err
}

// builtShape is the canonical FTS shape of a built index's in-memory table.
func builtShape(i Index) Index {
	return Index{Table: builtTable, Collection: i.Collection, Columns: i.Columns, SnippetColumn: i.SnippetColumn, Weights: i.Weights}
}

// validWhere admits one parenthesizable expression: no statement separators,
// comments or parameters outside string literals, and balanced parentheses,
// so the predicate cannot escape the "(...)" it is wrapped in. Identifiers
// must be bare: SQLite would read an unknown double-quoted identifier as a
// string literal instead of failing. SQLite then parses the predicate
// and the engine's authorizer limits what it may read; startup prepares every
// read and rejects any that binds a parameter of its own.
func validWhere(s string) error {
	invalid := fmt.Errorf("%w: where must be one SQL expression without parameters or comments", ErrIndexInvalid)
	if strings.TrimSpace(s) == "" || len(s) > 1024 || !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
		return invalid
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			closed := false
			for i++; i < len(s); i++ {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
			}
			if !closed {
				return invalid
			}
		case ';', '?', ':', '@', '$', '"', '`', '[':
			return invalid
		case '-', '/':
			if i+1 < len(s) && (s[i+1] == '-' && c == '-' || s[i+1] == '*' && c == '/') {
				return invalid
			}
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return invalid
			}
		}
	}
	if depth != 0 {
		return invalid
	}
	return nil
}

// BuiltSources lists, per table, the columns a built index reads (besides
// columns its Where reads), first naming the table.
func BuiltSources(i Index) [][]string {
	sources := [][]string{append([]string{i.Collection, "id"}, i.Columns...)}
	if i.Scope != nil {
		sources = append(sources, []string{i.Scope.Collection, "id", i.Scope.Field})
	}
	return sources
}

// prepareBuilt builds the fixed reads and in-memory statements of a built
// index, checks them on scratch, and extends the authorizer allowlist with
// exactly the columns they read.
func prepareBuilt(scratch *sql.DB, idx Index, readable map[string][]string, allowed map[string]map[string]bool) (definition, error) {
	if err := validateBuilt(idx); err != nil {
		return definition{}, err
	}
	if idx.Scope != nil {
		copied := *idx.Scope
		idx.Scope = &copied
		if idx.Members == "" {
			idx.Members = MembersAll
		}
	}
	allow := func(table string, columns ...string) {
		name := strings.ToLower(table)
		if allowed[name] == nil {
			allowed[name] = map[string]bool{}
		}
		for _, c := range columns {
			allowed[name][strings.ToLower(c)] = true
		}
	}
	def := definition{index: idx}
	for _, fields := range BuiltSources(idx) {
		allow(fields[0], fields[1:]...)
		def.sources = append(def.sources, fields[0])
	}
	if idx.Where != "" {
		// The predicate may read the collection's SQL-readable columns.
		allow(idx.Collection, readable[idx.Collection]...)
	}
	source := quote(idx.Collection) + "."
	read := []string{source + quote("id")}
	for _, c := range idx.Columns {
		read = append(read, source+quote(c))
	}
	var conds []string
	if idx.Where != "" {
		conds = append(conds, "(\n"+idx.Where+"\n)")
	}
	if idx.Scope != nil {
		conds = append(conds, source+quote("id")+" IN (SELECT value FROM json_each(?))")
		field := quote(idx.Scope.Collection) + "." + quote(idx.Scope.Field)
		def.scopeRead = "SELECT typeof(" + field + ")," + field + " FROM " + quote(idx.Scope.Collection) + " WHERE " + quote(idx.Scope.Collection) + "." + quote("id") + "=?"
	}
	def.rowsRead = "SELECT " + strings.Join(read, ",") + " FROM " + quote(idx.Collection)
	if len(conds) > 0 {
		def.rowsRead += " WHERE " + strings.Join(conds, " AND ")
	}
	ddl, err := CanonicalDDL(builtShape(idx))
	if err != nil {
		return definition{}, err
	}
	cols := []string{`"record_id"`}
	marks := []string{"?"}
	weights := []string{"0"}
	snip := 0
	for n, c := range idx.Columns {
		cols = append(cols, quote(c))
		marks = append(marks, "?")
		w := 1.0
		if len(idx.Weights) > 0 {
			w = idx.Weights[n]
		}
		weights = append(weights, fmt.Sprintf("%g", w))
		if c == idx.SnippetColumn {
			snip = n + 1
		}
	}
	table := quote(builtTable)
	def.memDDL = ddl
	def.memInsert = "INSERT INTO " + table + "(" + strings.Join(cols, ",") + ") VALUES(" + strings.Join(marks, ",") + ")"
	// Same fixed ranking and snippet query as persisted scoped search.
	def.memSearch = "SELECT record_id, bm25(" + table + "," + strings.Join(weights, ",") + "), snippet(" + table + fmt.Sprintf(",%d,'','',' … ',32) FROM ", snip) + table + " WHERE " + table + " MATCH ? ORDER BY 2, record_id LIMIT ? OFFSET ?"
	conn, err := scratch.Conn(context.Background())
	if err != nil {
		return definition{}, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), ddl); err != nil {
		return definition{}, fmt.Errorf("%w: %v", ErrIndexInvalid, err)
	}
	defer conn.ExecContext(context.Background(), "DROP TABLE "+table)
	for _, q := range []string{def.memInsert, def.memSearch} {
		stmt, err := conn.PrepareContext(context.Background(), q)
		if err != nil {
			return definition{}, fmt.Errorf("%w: %v", ErrIndexInvalid, err)
		}
		stmt.Close()
	}
	return def, nil
}

// checkBuiltReads prepares a built index's fixed reads against the database
// under the engine's authorizer, so a Where that reads anything else, or binds
// a parameter, fails at startup rather than on the first search.
func (e *Engine) checkBuiltReads(ctx context.Context, d definition) error {
	if !Built(d.index) {
		return nil
	}
	inputs := 0
	reads := map[string]int{}
	if d.scopeRead != "" {
		reads[d.scopeRead] = 1
		inputs = 1
	}
	reads[d.rowsRead] = inputs
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(raw any) error {
		c := raw.(readConn)
		for q, inputs := range reads {
			stmt, err := c.PrepareContext(ctx, q)
			if err != nil {
				return err
			}
			n := stmt.NumInput()
			stmt.Close()
			if n != inputs {
				return errors.New("where must not bind parameters")
			}
		}
		return nil
	})
}

// searchBuilt reads the index rows, then searches a cached or newly built
// in-memory index whose key is the fingerprint of exactly those rows.
func (e *Engine) searchBuilt(ctx context.Context, d definition, req Request) (Result, error) {
	result := Result{Index: req.Index, Hits: []Hit{}}
	if d.index.Scope == nil {
		if req.Scope != "" {
			return result, ErrInvalidQuery
		}
	} else if !validToken(req.Scope) {
		return result, ErrInvalidQuery
	}
	if req.Offset < 0 || req.Offset > MaxOffset || (req.Offset > 0 && req.ExpectedGeneration == "") || (req.ExpectedGeneration != "" && !validToken(req.ExpectedGeneration)) {
		return result, ErrInvalidQuery
	}
	match, err := matchExpression(req.Query)
	if err != nil {
		return result, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = min(DefaultLimit, e.limits.MaxRows)
	}
	if limit < 1 || limit > e.limits.MaxRows {
		return result, ErrInvalidQuery
	}
	// Admission precedes reading content, so queued requests retain no rows.
	waitCtx, waitCancel := context.WithTimeout(ctx, e.limits.Timeout)
	select {
	case e.requests <- struct{}{}:
		waitCancel()
	case <-waitCtx.Done():
		err := waitCtx.Err()
		waitCancel()
		return result, err
	}
	defer func() { <-e.requests }()
	readDone := tracing.Start(ctx, "search.read")
	rows, err := e.readBuilt(ctx, d, req.Scope)
	readDone()
	if err != nil {
		return result, err
	}
	generation := builtFingerprint(d.index, req.Scope, rows)
	if req.ExpectedGeneration != "" && req.ExpectedGeneration != generation {
		return result, ErrGenerationConflict
	}
	result.Scope, result.Generation = req.Scope, generation
	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	entry := e.cache.acquire(generation)
	if entry == nil {
		buildDone := tracing.Start(ctx, "search.build")
		built, err := e.build(ctx, d, rows)
		buildDone()
		if err != nil {
			if ctx.Err() != nil {
				return Result{Index: req.Index, Hits: []Hit{}}, ctx.Err()
			}
			return Result{Index: req.Index, Hits: []Hit{}}, err
		}
		entry = e.cache.insert(generation, built)
	}
	defer e.cache.release(entry)
	queryDone := tracing.Start(ctx, "search.query")
	hits, more, err := e.queryBuilt(ctx, entry, d, match, limit, req.Offset)
	queryDone()
	if err != nil {
		// A failed pinned connection is never reused or replaced silently.
		e.cache.remove(entry)
		if ctx.Err() != nil {
			return Result{Index: req.Index, Hits: []Hit{}}, ctx.Err()
		}
		return Result{Index: req.Index, Hits: []Hit{}}, err
	}
	for _, hit := range hits {
		result.Hits = append(result.Hits, hit)
		if encoded, _ := json.Marshal(result); len(encoded) > e.limits.MaxBytes {
			return Result{Index: req.Index, Hits: []Hit{}}, ErrResponseLimit
		}
	}
	result.HasMore, result.Truncated = more, more
	if encoded, _ := json.Marshal(result); len(encoded) > e.limits.MaxBytes {
		return Result{Index: req.Index, Hits: []Hit{}}, ErrResponseLimit
	}
	return result, nil
}

// readBuilt reads a built index's rows in one read transaction of the live
// database, bounded by the search timeout so it never pins a WAL snapshot for
// longer. Rows are sorted by ID.
func (e *Engine) readBuilt(ctx context.Context, d definition, scope string) ([]builtRow, error) {
	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	rows, err := func() ([]builtRow, error) {
		tx, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err = e.validate(ctx, tx, d); err != nil {
			return nil, err
		}
		return e.readIndexRows(ctx, tx, d, scope)
	}()
	var se sqlite3.Error
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, ErrScopeNotFound), errors.Is(err, ErrIndexInvalid), errors.Is(err, ErrIndexLimit):
		return nil, err
	case errors.As(err, &se) && (se.Code == sqlite3.ErrTooBig || se.Code == sqlite3.ErrFull):
		return nil, fmt.Errorf("%w: value size", ErrIndexLimit)
	case errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked):
		return nil, fmt.Errorf("%w: %w", ErrBusy, err)
	default:
		return nil, fmt.Errorf("%w: index read failed", ErrIndexInvalid)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	return rows, nil
}

// rowBudget charges indexed text against maxIndexBytes.
type rowBudget struct{ used, max int }

func (b *rowBudget) add(row builtRow) error {
	b.used += len(row.id)
	for _, v := range row.values {
		b.used += len(v)
	}
	if b.used > b.max {
		return fmt.Errorf("%w: maxIndexBytes", ErrIndexLimit)
	}
	return nil
}

// textValue accepts SQLite TEXT (valid UTF-8) or NULL as indexable content.
func textValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		if utf8.ValidString(x) {
			return x, nil
		}
	}
	return "", fmt.Errorf("%w: indexed values must be UTF-8 text or null", ErrIndexInvalid)
}

// readIndexRows reads the scope members (the values of the scope field's JSON
// object), then the matching rows that pass Where.
func (e *Engine) readIndexRows(ctx context.Context, tx *sql.Tx, d definition, scope string) ([]builtRow, error) {
	var args []any
	var ids []string
	if d.index.Scope != nil {
		var kind string
		var raw any
		err := tx.QueryRowContext(ctx, d.scopeRead, scope).Scan(&kind, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrScopeNotFound
		}
		if err != nil {
			return nil, err
		}
		text, ok := raw.(string)
		if kind != "text" || !ok {
			return nil, fmt.Errorf("%w: scope membership", ErrIndexInvalid)
		}
		members, err := scopeMembers(text)
		if err != nil {
			return nil, err
		}
		unique := map[string]bool{}
		ids = make([]string, 0, len(members))
		for _, id := range members {
			if !unique[id] {
				unique[id] = true
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		data, _ := json.Marshal(ids)
		args = append(args, string(data))
	}
	result, err := tx.QueryContext(ctx, d.rowsRead, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	var rows []builtRow
	seen := map[string]bool{}
	budget := rowBudget{max: e.maxIndexBytes}
	values := make([]any, len(d.index.Columns)+1)
	ptrs := make([]any, len(values))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for result.Next() {
		if err := result.Scan(ptrs...); err != nil {
			return nil, err
		}
		id, ok := values[0].(string)
		if !ok || !validToken(id) || seen[id] {
			return nil, fmt.Errorf("%w: record IDs must be unique nonempty text", ErrIndexInvalid)
		}
		seen[id] = true
		row := builtRow{id: id, values: make([]string, len(values)-1)}
		for i, v := range values[1:] {
			if row.values[i], err = textValue(v); err != nil {
				return nil, err
			}
		}
		if err := budget.add(row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	if ids != nil && d.index.Members == MembersAll && len(rows) != len(ids) {
		// Never return partial results for a scope that requires all members.
		return nil, fmt.Errorf("%w: scope member unavailable", ErrIndexInvalid)
	}
	return rows, nil
}
