package client

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/jsvm"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/appconfig"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/server"
)

const mcpDomain = "template.example.test"

type mcpFixture struct {
	t      *testing.T
	app    core.App
	public http.Handler
	bearer string
	reader string
}

// newMCPFixture composes the real public handler, OAuth and the MCP adapter
// around the application's real hooks, migrations and SQL server.
func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	jsvm.MustRegister(app, appconfig.Runtime.HooksConfig(root, ""))
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	server.RegisterDatabase(app, filepath.Join(root, "server.json"), filepath.Join(app.DataDir(), "data.db"))
	var router http.Handler
	adapter := newMCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	test, err := appconfig.Runtime.NewTestServer(app, appserver.TestServerOptions{Domain: mcpDomain, Cache: t.TempDir(), MCP: true, MCPHandler: adapter})
	if err != nil {
		t.Fatal(err)
	}
	router = test.Router()
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	account := core.NewRecord(users)
	account.SetEmail(appconfig.Identity.LocalAccountEmail)
	account.SetPassword("a stronger synthetic password")
	if err := app.Save(account); err != nil {
		t.Fatal(err)
	}
	reader, err := account.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	f := &mcpFixture{t: t, app: app, public: test.Public(), reader: reader}
	f.bearer = f.oauthToken()
	return f
}

func (f *mcpFixture) send(method, path, authorization, contentType, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, "https://"+mcpDomain+path, strings.NewReader(body))
	request.Host = mcpDomain
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	f.public.ServeHTTP(response, request)
	return response
}

// oauthToken walks the real flow: registration, reader consent, code exchange.
func (f *mcpFixture) oauthToken() string {
	f.t.Helper()
	callback := "https://client.example.test/callback"
	registered := f.send("POST", "/oauth/register", "", "application/json", `{"client_name":"synthetic","redirect_uris":["`+callback+`"],"token_endpoint_auth_method":"none"}`)
	if registered.Code != 201 {
		f.t.Fatalf("register: %d %s", registered.Code, registered.Body)
	}
	var client struct {
		ID string `json:"client_id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &client); err != nil {
		f.t.Fatal(err)
	}
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {callback}, "response_type": {"code"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	approved := f.send("POST", "/oauth/authorize?"+q.Encode(), f.reader, "application/json", `{"approve":true}`)
	if approved.Code != 200 {
		f.t.Fatalf("authorize: %d %s", approved.Code, approved.Body)
	}
	var redirect struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(approved.Body.Bytes(), &redirect); err != nil {
		f.t.Fatal(err)
	}
	target, err := url.Parse(redirect.Redirect)
	if err != nil {
		f.t.Fatal(err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ID}, "code": {target.Query().Get("code")}, "redirect_uri": {callback}, "code_verifier": {verifier}}
	exchanged := f.send("POST", "/oauth/token", "", "application/x-www-form-urlencoded", form.Encode())
	if exchanged.Code != 200 {
		f.t.Fatalf("token: %d %s", exchanged.Code, exchanged.Body)
	}
	var tokens struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(exchanged.Body.Bytes(), &tokens); err != nil || tokens.Access == "" {
		f.t.Fatalf("token response: %v %s", err, exchanged.Body)
	}
	return "Bearer " + tokens.Access
}

func (f *mcpFixture) rpc(method string, params any) map[string]any {
	f.t.Helper()
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	response := f.send("POST", "/mcp", f.bearer, "application/json", string(payload))
	if response.Code != 200 {
		f.t.Fatalf("MCP %s: %d %s", method, response.Code, response.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// call returns a tool's decoded JSON result and whether it was a tool error.
func (f *mcpFixture) call(name string, args map[string]any) (any, bool) {
	f.t.Helper()
	out := f.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	if out["error"] != nil {
		f.t.Fatalf("protocol error from %s: %v", name, out["error"])
	}
	result, _ := out["result"].(map[string]any)
	isError, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		f.t.Fatalf("%s: content %v", name, content)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var data any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		data = text
	}
	return data, isError
}

func (f *mcpFixture) must(name string, args map[string]any) map[string]any {
	f.t.Helper()
	data, isError := f.call(name, args)
	if isError {
		f.t.Fatalf("%s failed: %v", name, data)
	}
	out, _ := data.(map[string]any)
	return out
}

func (f *mcpFixture) refused(name string, args map[string]any, reason string) {
	f.t.Helper()
	data, isError := f.call(name, args)
	if !isError || !strings.Contains(strings.ToLower(jsonText(data)), reason) {
		f.t.Errorf("%s %v: error=%v %v; want %q", name, args, isError, data, reason)
	}
}

func jsonText(v any) string { s, _ := json.Marshal(v); return string(s) }

func TestMCPToolsAreExactlyFiveWithHints(t *testing.T) {
	f := newMCPFixture(t)
	result, _ := f.rpc("tools/list", map[string]any{})["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	wantDestructive := map[string]bool{"read": false, "search": false, "schema": false, "query": false, "write": true}
	if len(tools) != len(wantDestructive) {
		t.Fatalf("tools: %v", tools)
	}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		destructive, known := wantDestructive[name]
		if !known {
			t.Errorf("unexpected tool %s", name)
		}
		annotations, _ := tool["annotations"].(map[string]any)
		if annotations["destructiveHint"] != destructive || annotations["readOnlyHint"] == destructive {
			t.Errorf("%s: annotations %v", name, annotations)
		}
	}
}

func TestMCPToolsWorkThroughRecordsAPI(t *testing.T) {
	f := newMCPFixture(t)
	created := f.must("write", map[string]any{"operation": "create", "collection": "greetings", "fields": map[string]any{"message": "synthetic hello"}})
	id, _ := created["id"].(string)
	if !idPattern.MatchString(id) || created["message"] != "synthetic hello" {
		t.Fatalf("create: %v", created)
	}
	if got := f.must("read", map[string]any{"collection": "greetings", "id": id}); got["message"] != "synthetic hello" || got["collectionId"] != nil {
		t.Fatalf("read: %v", got)
	}
	updated := f.must("write", map[string]any{"operation": "update", "collection": "greetings", "id": id, "fields": map[string]any{"message": "synthetic changed"}})
	if updated["message"] != "synthetic changed" {
		t.Fatalf("update: %v", updated)
	}
	schema := f.must("schema", map[string]any{})
	if text := jsonText(schema["tables"]); !strings.Contains(text, `"greetings"`) || strings.Contains(text, `"users"`) {
		t.Fatalf("schema: %v", schema)
	}
	rows := f.must("query", map[string]any{"sql": "SELECT message FROM greetings"})
	if !strings.Contains(jsonText(rows), "synthetic changed") {
		t.Fatalf("query: %v", rows)
	}
	found := f.must("search", map[string]any{"query": "changed"})
	if !strings.Contains(jsonText(found), id) {
		t.Fatalf("search: %v", found)
	}
	if deleted := f.must("write", map[string]any{"operation": "delete", "collection": "greetings", "id": id}); deleted["deleted"] != true {
		t.Fatalf("delete: %v", deleted)
	}
	_, gone := f.call("read", map[string]any{"collection": "greetings", "id": id})
	if !gone {
		t.Fatal("deleted record still reads")
	}
}

func TestMCPToolsRefuseOutsideAllowlist(t *testing.T) {
	f := newMCPFixture(t)
	id := "abc123def456789"
	f.refused("read", map[string]any{"collection": "users", "id": id}, "not listed")
	f.refused("read", map[string]any{"collection": "_superusers", "id": id}, "not listed")
	f.refused("read", map[string]any{"collection": "greetings", "id": "../users"}, "id must be")
	f.refused("write", map[string]any{"operation": "delete", "collection": "users", "id": id}, "not listed")
	f.refused("write", map[string]any{"operation": "create", "collection": "greetings", "fields": map[string]any{"message": "x", "password": "y"}}, "not listed")
	f.refused("write", map[string]any{"operation": "truncate", "collection": "greetings"}, "operation")
	f.refused("write", map[string]any{"operation": "delete", "collection": "greetings", "id": id, "fields": map[string]any{"message": "x"}}, "does not take")
	f.refused("search", map[string]any{"index": "users", "query": "x"}, "configured")
	// The SQL reader refuses credentials and writes itself.
	for _, sql := range []string{"SELECT password FROM users", "DELETE FROM greetings", "SELECT * FROM sqlite_schema"} {
		if _, isError := f.call("query", map[string]any{"sql": sql}); !isError {
			t.Errorf("query %q succeeded", sql)
		}
	}
}

// TestMCPRejectsAnonymous: no OAuth token, no tools, and the reader's own token
// is not a substitute.
func TestMCPRejectsAnonymous(t *testing.T) {
	f := newMCPFixture(t)
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for name, authorization := range map[string]string{"none": "", "garbage": "Bearer not-a-token", "reader token": "Bearer " + f.reader} {
		response := f.send("POST", "/mcp", authorization, "application/json", payload)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Header().Get("WWW-Authenticate"), "resource_metadata") {
			t.Errorf("%s: %d %q", name, response.Code, response.Header().Get("WWW-Authenticate"))
		}
	}
	if status := f.send("POST", "/oauth/authorize?client_id=x", "", "application/json", `{"approve":true}`).Code; status < 400 {
		t.Errorf("anonymous approval = %d", status)
	}
}

// The adapter refuses a request that did not come through the public handler.
func TestMCPRequiresPublicBoundary(t *testing.T) {
	handler := newMCPHandler(http.NotFoundHandler())
	request := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", response.Code)
	}
}

func TestMCPAPIRouteLabels(t *testing.T) {
	cfg, err := appconfig.LoadPublicConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/context/schema", "/api/context/schema"},
		{"POST", "/api/context/query", "/api/context/query"},
		{"POST", "/api/collections/greetings/records", "/api/collections/{collection}/records"},
		{"DELETE", "/api/collections/greetings/records/abc", "/api/collections/{collection}/records/{id}"},
		{"GET", "/api/collections/greetings/records", "unknown"},
		{"POST", "/api/collections/users/records", "unknown"},
		{"GET", "/api/settings", "unknown"},
		{"POST", "/api/batch", "unknown"},
	} {
		if got := mcpAPIRoute(cfg, c.method, c.path); got != c.want {
			t.Errorf("%s %s = %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestValidateHTTPSArgs(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"--mcp"}, "--mcp requires"},
		{[]string{"--acme-email", "a@example.test"}, "--acme-email requires"},
		{[]string{"--headless"}, "--headless requires"},
		{[]string{"--port", "9443"}, "unknown option"},
		{[]string{"--domain", "example.test"}, "--domain requires"},
		{[]string{"--domain", "example.test", "--headless", "--tcp-only"}, "cannot be combined"},
		{[]string{"--domain", "example.test", "--headless", "--port", "9443"}, "cannot be combined"},
		{[]string{"--domain", "example.test", "--headless", "--ui-dev", "http://127.0.0.1:5173"}, "cannot be combined"},
		{[]string{"--domain", "https://example.test", "--headless"}, "domain must"},
		{[]string{"--domain", "example.test", "--headless", "--acme-email", "Name <a@example.test>"}, "plain email"},
	} {
		a, err := parseArgs(c.argv, []string{"ui-dev", "port", "domain", "acme-email"}, []string{"take-over", "tcp-only", "headless", "mcp"})
		if err != nil {
			t.Fatal(err)
		}
		err = validateHTTPSArgs(a)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.argv, err, c.want)
		}
	}
	for _, argv := range [][]string{nil, {"--domain", "example.test", "--headless"}, {"--domain", "example.test", "--headless", "--mcp", "--acme-email", "a@example.test"}} {
		a, _ := parseArgs(argv, []string{"ui-dev", "port", "domain", "acme-email"}, []string{"take-over", "tcp-only", "headless", "mcp"})
		if err := validateHTTPSArgs(a); err != nil {
			t.Errorf("%v: %v", argv, err)
		}
	}
}

func TestPublicHandoffRefusesOtherOrigin(t *testing.T) {
	code := strings.Repeat("a", 48)
	base := "https://" + mcpDomain
	if _, err := publicReaderHandoff(base, map[string]any{"url": base + "/bundle/login#" + code}); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"https://evil.example.test/bundle/login#" + code, base + "/bundle/login#short", ""} {
		if _, err := publicReaderHandoff(base, map[string]any{"url": link}); err == nil {
			t.Errorf("accepted %q", link)
		}
	}
}
