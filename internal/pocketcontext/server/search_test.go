package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/searchread"
)

func searchFixture(t *testing.T) (*tests.TestApp, Config, string, string) {
	t.Helper()
	app, token, outsider := fixture(t)
	index := searchread.Index{Table: "deal_search", Collection: "deals", Columns: []string{"title"}, SnippetColumn: "title", Weights: []float64{1}}
	// Trusted application migrations own index creation and maintenance.
	ddl, err := searchread.CanonicalDDL(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(ddl).Execute(); err != nil {
		t.Fatal(err)
	}
	collection, _ := app.FindCollectionByNameOrId("deals")
	for i, title := range []string{"Synthetic rocket project", "Synthetic rocket launch"} {
		record := core.NewRecord(collection)
		record.Set("id", fmt.Sprintf("searchrecord%03d", i))
		record.Set("title", title)
		record.Set("secret", "unsearchable-private-value")
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		if _, err := app.DB().NewQuery(`INSERT INTO deal_search(record_id,title) VALUES({:id},{:title})`).Bind(dbx.Params{"id": record.Id, "title": title}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{AuthCollection: "agents", Tables: map[string][]string{"deals": {"id", "title"}}, TimeoutMS: 1000, MaxRows: 100, MaxBytes: 4096, Search: &searchread.Config{Indexes: map[string]searchread.Index{"deals": index}}}
	return app, cfg, token, outsider
}

func TestSearchHTTP(t *testing.T) {
	app, cfg, token, outsider := searchFixture(t)
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId("_superusers")
	if err != nil {
		t.Fatal(err)
	}
	superuser := core.NewRecord(collection)
	superuser.SetEmail("search-superuser@example.com")
	superuser.SetPassword("synthetic-password-123")
	if err := app.Save(superuser); err != nil {
		t.Fatal(err)
	}
	superToken, err := superuser.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"", outsider, superToken} {
		r := request(h, "POST", "/api/context/search", identity, `{"index":"deals","query":"rocket"}`)
		if r.Code != http.StatusUnauthorized && r.Code != http.StatusForbidden {
			t.Fatalf("unauthorized search: %d %s", r.Code, r.Body)
		}
	}
	r := request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket","limit":1}`)
	var result searchread.Result
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusOK || result.Index != "deals" || len(result.Hits) != 1 || !result.Truncated || !strings.Contains(result.Hits[0].Excerpt, "rocket") || !strings.HasPrefix(result.Hits[0].ID, "searchrecord") {
		t.Fatalf("search: %d %s", r.Code, r.Body)
	}
	if r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("X-Context-Truncated") != "true" || !strings.Contains(r.Header().Get("Access-Control-Expose-Headers"), "X-Context-Truncated") {
		t.Fatalf("search headers: %v", r.Header())
	}
	r = request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"unsearchable-private-value"}`)
	if r.Code != http.StatusOK || strings.Contains(r.Body.String(), "searchrecord") {
		t.Fatalf("private text searchable: %d %s", r.Code, r.Body)
	}
	schema := request(h, "GET", "/api/context/schema", token, "")
	if schema.Code != http.StatusOK || !strings.Contains(schema.Body.String(), `"indexes":["deals"]`) || !strings.Contains(schema.Body.String(), `"maxTerms":16`) || strings.Contains(schema.Body.String(), "deal_search") || strings.Contains(schema.Body.String(), "secret") {
		t.Fatalf("search discovery: %d %s", schema.Code, schema.Body)
	}
	for _, query := range []string{
		`SELECT title FROM deal_search`,
		`SELECT * FROM deal_search_content`,
		`SELECT * FROM deal_search_config`,
		`SELECT * FROM deal_search_idx`,
		`SELECT * FROM deal_search_docsize`,
		`SELECT * FROM deal_search_data`,
		`SELECT name FROM sqlite_schema`,
		`PRAGMA data_version`,
	} {
		body, _ := json.Marshal(map[string]string{"sql": query})
		r := request(h, "POST", "/api/context/query", token, string(body))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("search internals exposed through SQL: %s: %d %s", query, r.Code, r.Body)
		}
	}
	for _, body := range []string{
		`{"index":"deals","query":"rocket","sql":"SELECT secret FROM deals"}`,
		`{"index":"deals","query":"rocket","limit":-1}`,
		`{"index":"deals","query":"rocket","limit":101}`,
		`{"index":"deals","query":"rocket","limit":"1"}`,
		`{"index":"deals","query":""}`,
		`{"index":"deals","query":"rocket"} {}`,
		`{"index":"deal_search_content","query":"rocket"}`,
		`{"index":"deals","query":"` + strings.Repeat("x", 4097) + `"}`,
		`{"index":"deals","query":"` + strings.Repeat("word ", 17) + `"}`,
		`{"index":"deals","query":"` + strings.Repeat("x", 65536) + `"}`,
		`null`,
		`[]`,
	} {
		r := request(h, "POST", "/api/context/search", token, body)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("bad search accepted: %d %s", r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "deal_search") || strings.Contains(r.Body.String(), "sqlite") {
			t.Fatalf("search error exposed implementation: %s", r.Body)
		}
	}
	if _, err := app.DB().NewQuery(`DROP TABLE deal_search`).Execute(); err != nil {
		t.Fatal(err)
	}
	r = request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket"}`)
	if r.Code != http.StatusServiceUnavailable || strings.Contains(r.Body.String(), "deal_search") || strings.Contains(r.Body.String(), "sqlite") {
		t.Fatalf("stale index failure: %d %s", r.Code, r.Body)
	}
}

func TestSearchRejectsUnsafeSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"hidden field", func(c *Config) {
			index := c.Search.Indexes["deals"]
			index.Columns = []string{"secret"}
			index.SnippetColumn = "secret"
			c.Search.Indexes["deals"] = index
		}},
		{"unexposed public field", func(c *Config) { c.Tables["deals"] = []string{"id"} }},
		{"unexposed record id", func(c *Config) { c.Tables["deals"] = []string{"title"} }},
		{"auth source", func(c *Config) {
			index := c.Search.Indexes["deals"]
			index.Collection = "agents"
			c.Search.Indexes["deals"] = index
		}},
		{"system source", func(c *Config) {
			index := c.Search.Indexes["deals"]
			index.Collection = "_collections"
			c.Search.Indexes["deals"] = index
		}},
		{"missing source", func(c *Config) {
			index := c.Search.Indexes["deals"]
			index.Collection = "unknown"
			c.Search.Indexes["deals"] = index
		}},
		{"collection as index", func(c *Config) {
			index := c.Search.Indexes["deals"]
			index.Table = "deals"
			c.Search.Indexes["deals"] = index
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, cfg, _, _ := searchFixture(t)
			test.change(&cfg)
			if _, err := startConfiguredRouter(t, app, cfg); err == nil {
				t.Fatal("unsafe search configuration accepted")
			}
		})
	}
}

func TestSearchImplicitPublicColumns(t *testing.T) {
	app, cfg, token, _ := searchFixture(t)
	collection, _ := app.FindCollectionByNameOrId("deals")
	collection.Fields.RemoveByName("secret")
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	cfg.Tables["deals"] = nil
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("implicit public columns: %d %s", r.Code, r.Body)
	}
}

func TestSearchDisabled(t *testing.T) {
	app, token, _ := fixture(t)
	h, err := startRouter(t, app, map[string][]string{"deals": {"id", "title"}})
	if err != nil {
		t.Fatal(err)
	}
	r := request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket"}`)
	if r.Code != http.StatusNotFound {
		t.Fatalf("unconfigured search enabled: %d %s", r.Code, r.Body)
	}
}

func TestSearchHTTPResponseLimit(t *testing.T) {
	app, cfg, token, _ := searchFixture(t)
	cfg.MaxBytes = 1024
	title := strings.Repeat("r", 1800)
	record, err := app.FindRecordById("deals", "searchrecord000")
	if err != nil {
		t.Fatal(err)
	}
	record.Set("title", title)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(`UPDATE deal_search SET title={:title} WHERE record_id='searchrecord000'`).Bind(dbx.Params{"title": title}).Execute(); err != nil {
		t.Fatal(err)
	}
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"index": "deals", "query": title})
	r := request(h, "POST", "/api/context/search", token, string(body))
	if r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("search byte cap: %d %s", r.Code, r.Body)
	}
}

func scopedSearchFixture(t *testing.T) (*tests.TestApp, Config, string) {
	app, cfg, token, _ := searchFixture(t)
	scopes := core.NewBaseCollection("search_scopes")
	scopes.Fields.Add(&core.JSONField{Name: "members"}, &core.TextField{Name: "private", Hidden: true})
	state := core.NewBaseCollection("search_state")
	state.Fields.Add(&core.TextField{Name: "generation"})
	for _, c := range []*core.Collection{scopes, state} {
		if err := app.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	r := core.NewRecord(scopes)
	r.Set("id", "scope0000000001")
	r.Set("members", map[string]string{"one": "searchrecord000", "two": "searchrecord001"})
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	r = core.NewRecord(state)
	r.Set("id", "state0000000001")
	r.Set("generation", "one")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	cfg.Tables["search_scopes"] = []string{"id", "members"}
	cfg.Tables["search_state"] = []string{"id", "generation"}
	idx := cfg.Search.Indexes["deals"]
	idx.Scope = &searchread.Scope{Collection: "search_scopes", Field: "members"}
	idx.Generation = &searchread.Generation{Collection: "search_state", Record: "state0000000001", Field: "generation"}
	cfg.Search.Indexes["deals"] = idx
	return app, cfg, token
}
func TestSearchScopedHTTP(t *testing.T) {
	app, cfg, token := scopedSearchFixture(t)
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket","scope":"scope0000000001","limit":1}`)
	var first searchread.Result
	if err = json.Unmarshal(r.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if r.Code != 200 || !first.HasMore || len(first.Generation) != 64 || first.Scope != "scope0000000001" {
		t.Fatalf("first: %d %s", r.Code, r.Body)
	}
	r = request(h, "POST", "/api/context/search", token, fmt.Sprintf(`{"index":"deals","query":"rocket","scope":"scope0000000001","limit":1,"offset":1,"expectedGeneration":%q}`, first.Generation))
	var second searchread.Result
	json.Unmarshal(r.Body.Bytes(), &second)
	if r.Code != 200 || second.HasMore || len(second.Hits) != 1 || second.Hits[0].ID == first.Hits[0].ID {
		t.Fatalf("second: %d %s", r.Code, r.Body)
	}
	state, err := app.FindRecordById("search_state", "state0000000001")
	if err != nil {
		t.Fatal(err)
	}
	state.Set("generation", "two")
	if err = app.Save(state); err != nil {
		t.Fatal(err)
	}
	r = request(h, "POST", "/api/context/search", token, fmt.Sprintf(`{"index":"deals","query":"rocket","scope":"scope0000000001","limit":1,"offset":1,"expectedGeneration":%q}`, first.Generation))
	if r.Code != 409 || strings.Contains(r.Body.String(), "searchrecord") {
		t.Fatalf("conflict: %d %s", r.Code, r.Body)
	}
	for _, body := range []string{`{"index":"deals","query":"rocket"}`, `{"index":"deals","query":"rocket","scope":"scope0000000001","offset":1}`} {
		r = request(h, "POST", "/api/context/search", token, body)
		if r.Code != 400 {
			t.Fatalf("missing scope/generation: %d %s", r.Code, r.Body)
		}
	}
	r = request(h, "POST", "/api/context/search", token, `{"index":"deals","query":"rocket","scope":"absent"}`)
	if r.Code != 503 {
		t.Fatalf("missing scope: %d %s", r.Code, r.Body)
	}
}
func TestSearchScopedAuthorization(t *testing.T) {
	for _, mode := range []string{"hidden", "unexposed", "auth", "system", "missing_generation", "missing_scope", "bad_record", "bad_field"} {
		t.Run(mode, func(t *testing.T) {
			app, cfg, _ := scopedSearchFixture(t)
			idx := cfg.Search.Indexes["deals"]
			switch mode {
			case "hidden":
				idx.Scope.Field = "private"
			case "unexposed":
				delete(cfg.Tables, "search_scopes")
			case "auth":
				idx.Scope.Collection = "agents"
			case "system":
				idx.Generation.Collection = "_superusers"
			case "missing_generation":
				idx.Generation = nil
			case "missing_scope":
				idx.Scope = nil
			case "bad_record":
				idx.Generation.Record = ""
			case "bad_field":
				idx.Scope.Field = "members;DROP TABLE deals"
			}
			cfg.Search.Indexes["deals"] = idx
			if _, err := startConfiguredRouter(t, app, cfg); err == nil {
				t.Fatal("unsafe scoped configuration accepted")
			}
		})
	}
}

// builtSearchFixture builds two indexes over synthetic PocketBase collections:
// published revisions by manifest value with archived revisions excluded, and
// every current (unarchived) revision without a scope.
func builtSearchFixture(t *testing.T) (*tests.TestApp, Config, string) {
	t.Helper()
	app, token, _ := fixture(t)
	for _, spec := range []struct {
		name   string
		fields []core.Field
	}{
		{"spaces", []core.Field{&core.TextField{Name: "name"}}},
		{"page_revisions", []core.Field{&core.TextField{Name: "space"}, &core.TextField{Name: "title"}, &core.TextField{Name: "summary"}, &core.TextField{Name: "body"}, &core.BoolField{Name: "archived"}, &core.TextField{Name: "notes", Hidden: true}}},
		{"publications", []core.Field{&core.TextField{Name: "space"}, &core.JSONField{Name: "manifest"}, &core.TextField{Name: "private", Hidden: true}}},
	} {
		c := core.NewBaseCollection(spec.name)
		c.Fields.Add(spec.fields...)
		if err := app.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	save := func(table, id string, values map[string]any) {
		c, err := app.FindCollectionByNameOrId(table)
		if err != nil {
			t.Fatal(err)
		}
		r := core.NewRecord(c)
		r.Set("id", id)
		for k, v := range values {
			r.Set(k, v)
		}
		if err := app.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	save("spaces", "spacealpha00001", map[string]any{"name": "Alpha"})
	save("spaces", "spacebravo00001", map[string]any{"name": "Bravo"})
	for _, r := range []struct {
		id, space, title, body string
		archived               bool
	}{
		{"revalpha0000001", "spacealpha00001", "Rocket guide", "synthetic rocket launch checklist", false},
		{"revalpha0000002", "spacealpha00001", "Garden", "rocket rocket in the garden", false},
		{"revalpha0000003", "spacealpha00001", "Retired", "archived rocket draft", true},
		{"revbravo0000001", "spacebravo00001", "Bravo rocket", "bravo rocket plans", false},
	} {
		save("page_revisions", r.id, map[string]any{"space": r.space, "title": r.title, "body": r.body, "archived": r.archived, "notes": "hidden rocket notes"})
	}
	save("publications", "publicalpha0001", map[string]any{"space": "spacealpha00001", "manifest": map[string]string{"pageguide000001": "revalpha0000001", "pagegarden00001": "revalpha0000002", "pageretired0001": "revalpha0000003"}})
	save("publications", "publicbravo0001", map[string]any{"space": "spacebravo00001", "manifest": map[string]string{"pagebravo000001": "revbravo0000001"}})
	cfg := Config{
		AuthCollection: "agents",
		Tables: map[string][]string{
			"spaces":         {"id", "name"},
			"page_revisions": {"id", "space", "title", "summary", "body", "archived"},
			"publications":   {"id", "space", "manifest"},
		},
		TimeoutMS: 1000, MaxRows: 100, MaxBytes: 8192,
		Search: &searchread.Config{Indexes: map[string]searchread.Index{
			"pages":   {Collection: "page_revisions", Columns: []string{"title", "summary", "body"}, SnippetColumn: "body", Weights: []float64{8, 3, 1}, Scope: &searchread.Scope{Collection: "publications", Field: "manifest"}, Members: "visible", Where: "archived = 0"},
			"current": {Collection: "page_revisions", Columns: []string{"title", "body"}, SnippetColumn: "body", Where: "archived = 0"},
		}},
	}
	return app, cfg, token
}

func searchHTTP(t *testing.T, h http.Handler, token, body string) (int, searchread.Result, string) {
	t.Helper()
	r := request(h, "POST", "/api/context/search", token, body)
	var result searchread.Result
	if r.Code == http.StatusOK {
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return r.Code, result, r.Body.String()
}

func resultIDs(r searchread.Result) string {
	out := []string{}
	for _, h := range r.Hits {
		out = append(out, h.ID)
	}
	return strings.Join(out, ",")
}

func TestBuiltSearchHTTP(t *testing.T) {
	app, cfg, token := builtSearchFixture(t)
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	code, first, body := searchHTTP(t, h, token, `{"index":"pages","query":"rocket","scope":"publicalpha0001","limit":1}`)
	if code != 200 || len(first.Hits) != 1 || !first.HasMore || len(first.Generation) != 64 || first.Scope != "publicalpha0001" {
		t.Fatalf("first page: %d %s", code, body)
	}
	page := fmt.Sprintf(`{"index":"pages","query":"rocket","scope":"publicalpha0001","limit":1,"offset":1,"expectedGeneration":%q}`, first.Generation)
	code, second, body := searchHTTP(t, h, token, page)
	if code != 200 || len(second.Hits) != 1 || second.HasMore || second.Hits[0].ID == first.Hits[0].ID || strings.Contains(body, "revalpha0000003") {
		t.Fatalf("second page (archived revision excluded): %d %s", code, body)
	}
	if code, r, body := searchHTTP(t, h, token, `{"index":"pages","query":"hidden notes","scope":"publicalpha0001"}`); code != 200 || len(r.Hits) != 0 {
		t.Fatalf("hidden field searchable: %d %s", code, body)
	}
	if code, _, body := searchHTTP(t, h, token, `{"index":"pages","query":"rocket","scope":"publicmissing01"}`); code != 404 || !strings.Contains(body, "Search scope not found") {
		t.Fatalf("missing scope: %d %s", code, body)
	}
	// Changes outside the scope keep the generation; indexed changes conflict.
	other, err := app.FindRecordById("page_revisions", "revbravo0000001")
	if err != nil {
		t.Fatal(err)
	}
	other.Set("body", "bravo rocket rocket")
	if err := app.Save(other); err != nil {
		t.Fatal(err)
	}
	if code, _, body := searchHTTP(t, h, token, page); code != 200 {
		t.Fatalf("unrelated change: %d %s", code, body)
	}
	garden, err := app.FindRecordById("page_revisions", "revalpha0000002")
	if err != nil {
		t.Fatal(err)
	}
	garden.Set("archived", true)
	if err := app.Save(garden); err != nil {
		t.Fatal(err)
	}
	if code, _, body := searchHTTP(t, h, token, page); code != 409 || strings.Contains(body, "rev") {
		t.Fatalf("archiving a member: %d %s", code, body)
	}
	if code, r, body := searchHTTP(t, h, token, `{"index":"pages","query":"rocket","scope":"publicalpha0001"}`); code != 200 || resultIDs(r) != "revalpha0000001" {
		t.Fatalf("after archiving: %d %s", code, body)
	}
	// A scope-less index searches every row that passes its where predicate.
	if code, r, body := searchHTTP(t, h, token, `{"index":"current","query":"rocket"}`); code != 200 || resultIDs(r) != "revbravo0000001,revalpha0000001" || r.Scope != "" || len(r.Generation) != 64 {
		t.Fatalf("scope-less index: %d %s", code, body)
	}
	if code, r, body := searchHTTP(t, h, token, `{"index":"current","query":"retired"}`); code != 200 || len(r.Hits) != 0 {
		t.Fatalf("archived revision searched: %d %s", code, body)
	}
	for _, req := range []string{
		`{"index":"pages","query":"rocket"}`,
		`{"index":"current","query":"rocket","scope":"publicalpha0001"}`,
		`{"index":"pages","query":"rocket","scope":"publicalpha0001","offset":1}`,
		`{"index":"pages","query":"rocket","requester":"x"}`,
	} {
		if code, _, body := searchHTTP(t, h, token, req); code != 400 {
			t.Fatalf("%s: %d %s", req, code, body)
		}
	}
	schema := request(h, "GET", "/api/context/schema", token, "").Body.String()
	if !strings.Contains(schema, `"maxIndexBytes":8388608`) || !strings.Contains(schema, `"indexes":["current","pages"]`) || !strings.Contains(schema, `"permissionModel":"shared"`) {
		t.Fatalf("discovery: %s", schema)
	}
	for _, leak := range []string{"isolation", "snapshot", "archived = 0", "built_search", "where"} {
		if strings.Contains(schema, leak) {
			t.Fatalf("discovery leaks %s: %s", leak, schema)
		}
	}
	for _, identity := range []string{"", "invalid"} {
		if r := request(h, "POST", "/api/context/search", identity, `{"index":"current","query":"rocket"}`); r.Code != 401 && r.Code != 403 {
			t.Fatalf("unauthenticated: %d %s", r.Code, r.Body)
		}
	}
}

func TestBuiltSearchHTTPLimits(t *testing.T) {
	app, cfg, token := builtSearchFixture(t)
	cfg.Search.MaxIndexBytes = 1024
	h, err := startConfiguredRouter(t, app, cfg)
	if err != nil {
		t.Fatal(err)
	}
	query := `{"index":"pages","query":"rocket","scope":"publicalpha0001"}`
	if code, _, body := searchHTTP(t, h, token, query); code != 200 {
		t.Fatalf("before: %d %s", code, body)
	}
	r, err := app.FindRecordById("page_revisions", "revalpha0000002")
	if err != nil {
		t.Fatal(err)
	}
	r.Set("body", strings.Repeat("rocket ", 200))
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	if code, _, body := searchHTTP(t, h, token, query); code != 413 || !strings.Contains(body, "Search content exceeds configured limits") || strings.Contains(body, "rev") {
		t.Fatalf("index limit: %d %s", code, body)
	}
}

func TestBuiltSearchHTTPRejectsConfiguration(t *testing.T) {
	for _, name := range []string{"unconfigured column", "hidden column", "unconfigured scope field", "hidden scope field", "where unconfigured column", "where hidden column", "where binds", "generation", "persisted with where", "bad limits", "limits without built index", "isolation"} {
		t.Run(name, func(t *testing.T) {
			app, cfg, _ := builtSearchFixture(t)
			idx := cfg.Search.Indexes["pages"]
			switch name {
			case "unconfigured column":
				cfg.Tables["page_revisions"] = []string{"id", "space", "title", "body", "archived"}
			case "hidden column":
				cfg.Tables["page_revisions"] = append(cfg.Tables["page_revisions"], "notes")
				idx.Columns, idx.SnippetColumn, idx.Weights = []string{"notes"}, "notes", nil
			case "unconfigured scope field":
				idx.Scope.Field = "space"
				cfg.Tables["publications"] = []string{"id", "manifest"}
			case "hidden scope field":
				cfg.Tables["publications"] = append(cfg.Tables["publications"], "private")
				idx.Scope.Field = "private"
			case "where unconfigured column":
				idx.Where = "created > ''"
			case "where hidden column":
				idx.Where = "notes = ''"
			case "where binds":
				idx.Where = "title = :title"
			case "generation":
				idx.Generation = &searchread.Generation{Collection: "publications", Record: "publicalpha0001", Field: "space"}
			case "persisted with where":
				idx.Table = "pages_search"
			case "bad limits":
				cfg.Search.MaxIndexBytes = searchread.MaxMaxIndexBytes + 1
			case "limits without built index":
				cfg.Search = &searchread.Config{Indexes: map[string]searchread.Index{"deals": {Table: "deal_search", Collection: "page_revisions", Columns: []string{"title"}, SnippetColumn: "title"}}, CacheBytes: 1 << 20}
			}
			if name != "limits without built index" {
				cfg.Search.Indexes["pages"] = idx
			}
			if name == "isolation" {
				// Per-requester isolation is gone; its configuration key is unknown.
				path := filepath.Join(t.TempDir(), "context.json")
				text := `{"authCollection":"agents","tables":{"page_revisions":["id","title"]},"search":{"indexes":{"pages":{"isolation":"private","collection":"page_revisions","columns":["title"],"snippetColumn":"title"}}}}`
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadConfig(path); err == nil {
					t.Fatal("isolation accepted")
				}
				return
			}
			if _, err := startConfiguredRouter(t, app, cfg); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

// The embedded server.json loads, has no snapshot block, and keeps every
// index built over configured columns.
func TestEmbeddedConfigurationLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join("..", "..", "..", "server.json"))
	if strings.Contains(string(data), `"snapshot"`) || strings.Contains(string(data), `"isolation"`) || cfg.Search == nil || len(cfg.Search.Indexes) == 0 {
		t.Fatalf("server.json: %+v", cfg)
	}
	for alias, index := range cfg.Search.Indexes {
		if !searchread.Built(index) {
			t.Fatalf("%s is not built", alias)
		}
	}
	if cfg.Search.MaxIndexBytes != 8388608 || cfg.Search.CacheBytes != 67108864 {
		t.Fatalf("limits: %+v", cfg.Search)
	}
}
