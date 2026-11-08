package appconfig

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/subscriptions"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver/routepolicytest"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/server"
)

const (
	publicDomain = "template.example.test"
	listed       = "greetings"
)

// publicAllow is the golden table of what the public origin allows.
func publicAllow() []routepolicytest.Case {
	var cases []routepolicytest.Case
	add := func(allow bool, method string, paths ...string) {
		for _, path := range paths {
			cases = append(cases, routepolicytest.Case{Method: method, Path: path, Allow: allow})
		}
	}
	records := "/api/collections/" + listed + "/records"
	record := records + "/abc123def456789"
	reader := []string{"/", "/bundle/login", "/bundle/login.js", "/api/bundle/auth-mode", "/assets/index-5P1JB3IQ.js", "/api/context/schema", "/api/realtime", records, record}
	add(true, "GET", reader...)
	add(true, "HEAD", reader...)
	add(true, "POST", "/api/bundle/redeem", "/api/collections/users/auth-with-password", "/api/collections/users/auth-refresh",
		"/api/context/query", "/api/context/search", "/api/realtime", records)
	add(true, "PATCH", record)
	add(true, "DELETE", record)
	// Near misses of the allowed routes.
	add(false, "GET", "/index.html", "/favicon.ico", "/assets/", "/assets/.hidden.js", "/assets/name.map", "/assets/x/y.js", "/api/bundle/version", "/api/health",
		"/api/context/query", "/api/collections/users/auth-with-password", "/oauth/authorize", "/mcp", records+"/", record+"/external-auths", "/api/files/"+listed+"/a/b")
	add(false, "POST", "/", "/api/context/schema", "/api/bundle/auth-mode", "/api/bundle/reader-code", record, "/api/collections/users/auth-with-otp", "/api/collections/users/request-otp", "/api/collections/users/request-password-reset", "/api/collections/_superusers/auth-with-password", "/oauth/token", "/oauth/register", "/mcp")
	add(false, "PUT", records, record, "/api/context/query")
	add(false, "DELETE", records, "/api/collections/users/records/abc123def456789", "/api/collections/_superusers/records/abc123def456789")
	add(false, "PATCH", records, "/api/collections/users/records/abc123def456789")
	add(false, "GET", "/api/collections/_superusers/records", "/api/collections/unlisted/records", "/api/collections/unlisted/records/abc123def456789")
	add(false, "POST", "/api/collections/unlisted/records", "/api/collections/_superusers/records")
	return cases
}

func TestPublicRouteTable(t *testing.T) {
	route := PublicRoute(nil)
	routepolicytest.Check(t, route, publicAllow())
	routepolicytest.Check(t, route, routepolicytest.CommonDenied())
}

// TestPublicRouteUnlistedCollection: the allowlist is the data, not the code.
func TestPublicRouteUnlistedCollection(t *testing.T) {
	cfg, err := ParsePublicConfig([]byte(`{"authCollection":"users","tables":{"notes":["id"]},"public":{"collections":["notes"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	route := publicRoute(cfg, nil)
	if !route("GET", "/api/collections/notes/records") {
		t.Error("listed collection denied")
	}
	if route("GET", "/api/collections/"+listed+"/records") {
		t.Error("unlisted collection allowed")
	}
	routepolicytest.Check(t, route, routepolicytest.CommonDenied())
}

func TestPublicRouteFailsClosed(t *testing.T) {
	bad := []struct{ name, config string }{
		{"not json", `{`},
		{"no public section", `{"authCollection":"users","tables":{"a":["id"]}}`},
		{"empty list", `{"authCollection":"users","tables":{"a":["id"]},"public":{"collections":[]}}`},
		{"no auth collection", `{"tables":{"a":["id"]},"public":{"collections":["a"]}}`},
		{"auth collection", `{"authCollection":"users","tables":{"users":["id"]},"public":{"collections":["users"]}}`},
		{"auth collection case", `{"authCollection":"users","tables":{"Users":["id"]},"public":{"collections":["Users"]}}`},
		{"system collection", `{"authCollection":"users","tables":{"_superusers":["id"]},"public":{"collections":["_superusers"]}}`},
		{"not in tables", `{"authCollection":"users","tables":{"a":["id"]},"public":{"collections":["a","b"]}}`},
		{"odd name", `{"authCollection":"users","tables":{"a/b":["id"]},"public":{"collections":["a/b"]}}`},
		{"duplicate", `{"authCollection":"users","tables":{"a":["id"]},"public":{"collections":["a","a"]}}`},
		{"table not listed", `{"authCollection":"users","tables":{"a":["id"],"b":["id"]},"public":{"collections":["a"]}}`},
	}
	for _, c := range bad {
		cfg, err := ParsePublicConfig([]byte(c.config))
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		}
		for _, path := range []string{"/", "/api/context/query", "/api/collections/a/records"} {
			if publicRoute(cfg, err)("GET", path) || publicRoute(cfg, err)("POST", path) {
				t.Errorf("%s: %s allowed", c.name, path)
			}
		}
	}
	unreadable := publicRoute(nil, errors.New("missing server.json"))
	for _, c := range publicAllow() {
		if unreadable(c.Method, c.Path) {
			t.Errorf("missing config allows %s %s", c.Method, c.Path)
		}
	}
}

func TestPublicPasswordRule(t *testing.T) {
	for password, want := range map[string]bool{
		"":                        false,
		"short":                   false,
		"elevenchars":             false,
		"aaaaaaaaaaaaaaaa":        false,
		"abababababababab":        false,
		"synthetic test password": true,
		"twelve chars":            true,
		"pässwörd-läng":           true,
	} {
		if got := publicPasswordStrong(password); got != want {
			t.Errorf("%q: %v, want %v", password, got, want)
		}
	}
}

func publicAccount(t *testing.T, app core.App, password string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(users)
	record.SetEmail(Identity.LocalAccountEmail)
	record.SetPassword(password)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPublicModeRefusesWeakAccounts(t *testing.T) {
	app := newHookApp(t)
	bindPublicGuards(app)
	cfg, err := LoadPublicConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPublicReady(app, cfg); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("no account: %v", err)
	}
	record := publicAccount(t, app, "synthetic")
	if err := checkPublicReady(app, cfg); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("weak password: %v", err)
	}
	record.SetPassword("a stronger synthetic password")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if err := checkPublicReady(app, cfg); err != nil {
		t.Fatalf("strong password: %v", err)
	}
	// A save that does not touch the password keeps the flag.
	reloaded, err := app.FindRecordById("users", record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Save(reloaded); err != nil {
		t.Fatal(err)
	}
	if err := checkPublicReady(app, cfg); err != nil {
		t.Fatalf("unrelated save: %v", err)
	}
	reloaded.SetPassword("weakagain")
	if err := app.Save(reloaded); err != nil {
		t.Fatal(err)
	}
	if err := checkPublicReady(app, cfg); err == nil {
		t.Fatal("weak password after a strong one was accepted")
	}
}

// An account from an earlier release has a hash but no flag: refused until changed.
func TestPublicModeRefusesAccountWithoutFlag(t *testing.T) {
	app := newHookApp(t)
	publicAccount(t, app, "a stronger synthetic password") // saved without the guards
	cfg, err := LoadPublicConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPublicReady(app, cfg); err == nil {
		t.Fatal("account without a recorded strength was accepted")
	}
}

func TestPublicModeRefusesAuthCollectionInDatabase(t *testing.T) {
	app := newHookApp(t)
	bindPublicGuards(app)
	publicAccount(t, app, "a stronger synthetic password")
	authTable := core.NewAuthCollection("members")
	if err := app.Save(authTable); err != nil {
		t.Fatal(err)
	}
	cfg := &PublicConfig{Collections: []string{"members"}, Columns: map[string][]string{"members": {"id"}}, authCollection: "users"}
	if err := checkPublicReady(app, cfg); err == nil || !strings.Contains(err.Error(), "base collection") {
		t.Fatalf("auth collection: %v", err)
	}
	cfg = &PublicConfig{Collections: []string{"absent"}, Columns: map[string][]string{"absent": {"id"}}, authCollection: "users"}
	if err := checkPublicReady(app, cfg); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing collection: %v", err)
	}
}

type publicFixture struct {
	t      *testing.T
	app    core.App
	server *appserver.TestServer
	token  string
}

func newPublicFixture(t *testing.T) *publicFixture {
	t.Helper()
	app := newHookApp(t)
	bindPublicGuards(app)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	server.RegisterDatabase(app, filepath.Join(root, "server.json"), filepath.Join(app.DataDir(), "data.db"))
	readerDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(readerDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readerDir, "index.html"), []byte("<html>synthetic</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	test, err := Runtime.NewTestServer(app, appserver.TestServerOptions{Domain: publicDomain, Cache: t.TempDir(), ReaderDir: readerDir})
	if err != nil {
		t.Fatal(err)
	}
	// The SQL reader closes on terminate; Windows cannot delete the database while it is open.
	t.Cleanup(func() { _ = app.OnTerminate().Trigger(&core.TerminateEvent{App: app}) })
	account := publicAccount(t, app, "a stronger synthetic password")
	token, err := account.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	return &publicFixture{t, app, test, token}
}

func (f *publicFixture) request(method, path, token, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, "https://"+publicDomain+path, strings.NewReader(body))
	request.Host = publicDomain
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	response := httptest.NewRecorder()
	f.server.Public().ServeHTTP(response, request)
	return response
}

func TestPublicHandlerAnonymous(t *testing.T) {
	f := newPublicFixture(t)
	records := "/api/collections/" + listed + "/records"
	if status := f.request("GET", "/", "", "").Code; status != 200 {
		t.Errorf("public GET / = %d, want 200", status)
	}
	if status := f.request("GET", "/api/bundle/auth-mode", "", "").Code; status != 200 {
		t.Errorf("auth-mode = %d, want 200", status)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/context/schema", ""},
		{"POST", "/api/context/query", `{"sql":"SELECT 1"}`},
		{"POST", "/api/context/search", `{"index":"` + listed + `","query":"hello"}`},
		{"GET", records + "/abc123def456789", ""},
		{"POST", records, `{"message":"anonymous"}`},
		{"PATCH", records + "/abc123def456789", `{"message":"anonymous"}`},
		{"DELETE", records + "/abc123def456789", ""},
		{"POST", "/api/collections/users/auth-refresh", `{}`},
	} {
		status := f.request(c.method, c.path, "", c.body).Code
		if status < 400 || status >= 500 {
			t.Errorf("anonymous %s %s = %d", c.method, c.path, status)
		}
	}
	// A collection rule answers a list with an empty page, never the records.
	if created := f.request("POST", records, f.token, `{"message":"synthetic secret"}`); created.Code != 200 {
		t.Fatalf("seed: %d %s", created.Code, created.Body)
	}
	list := f.request("GET", records, "", "")
	if strings.Contains(list.Body.String(), "synthetic secret") {
		t.Errorf("anonymous list shows records: %d %s", list.Code, list.Body)
	}
	// Without --mcp core exposes no OAuth or MCP route, and the policy adds none.
	for _, path := range []string{"/mcp", "/oauth/authorize", "/.well-known/oauth-authorization-server"} {
		if status := f.request("GET", path, "", "").Code; status != 404 {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
	for _, path := range []string{"/_/", "/api/health", "/api/settings", "/api/collections", "/api/bundle/instance"} {
		if status := f.request("GET", path, f.token, "").Code; status != 404 {
			t.Errorf("signed-in GET %s = %d, want 404", path, status)
		}
	}
	if status := f.request("POST", "/api/batch", f.token, `{"requests":[]}`).Code; status != 404 {
		t.Errorf("batch = %d, want 404", status)
	}
}

func TestPublicHandlerAuthenticated(t *testing.T) {
	f := newPublicFixture(t)
	records := "/api/collections/" + listed + "/records"
	out := f.request("POST", "/api/collections/users/auth-with-password", "", `{"identity":"`+Identity.LocalAccountEmail+`","password":"a stronger synthetic password"}`)
	if out.Code != 200 {
		t.Fatalf("password sign-in = %d %s", out.Code, out.Body)
	}
	if strings.Contains(out.Body.String(), PublicPasswordField) {
		t.Error("sign-in response exposes the password strength flag")
	}
	if status := f.request("POST", "/api/collections/users/auth-with-password", "", `{"identity":"`+Identity.LocalAccountEmail+`","password":"wrong password"}`).Code; status < 400 {
		t.Errorf("wrong password = %d", status)
	}
	created := f.request("POST", records, f.token, `{"message":"synthetic hello"}`)
	if created.Code != 200 {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var record struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if status := f.request("GET", records+"/"+record.ID, f.token, "").Code; status != 200 {
		t.Errorf("read = %d", status)
	}
	if status := f.request("PATCH", records+"/"+record.ID, f.token, `{"message":"changed"}`).Code; status != 200 {
		t.Errorf("update = %d", status)
	}
	if status := f.request("GET", "/api/context/schema", f.token, "").Code; status != 200 {
		t.Errorf("schema = %d", status)
	}
	query := f.request("POST", "/api/context/query", f.token, `{"sql":"SELECT 1 AS one"}`)
	if query.Code != 200 {
		t.Errorf("query = %d %s", query.Code, query.Body)
	}
	if status := f.request("DELETE", records+"/"+record.ID, f.token, "").Code; status != 204 {
		t.Errorf("delete = %d", status)
	}
	// Signed in or not, the users collection and unlisted collections stay closed.
	if status := f.request("GET", "/api/collections/users/records", f.token, "").Code; status != 404 {
		t.Errorf("users = %d", status)
	}
	unlisted := core.NewBaseCollection("private_notes")
	unlisted.ListRule = new(string)
	if err := f.app.Save(unlisted); err != nil {
		t.Fatal(err)
	}
	if status := f.request("GET", "/api/collections/private_notes/records", f.token, "").Code; status != 404 {
		t.Errorf("unlisted collection = %d", status)
	}
}

// Realtime carries record events, so subscriptions honour the allowlist too.
func TestPublicRealtimeAllowlist(t *testing.T) {
	f := newPublicFixture(t)
	client := subscriptions.NewDefaultClient()
	f.app.SubscriptionsBroker().Register(client)
	subscribe := func(topic string) int {
		body, _ := json.Marshal(map[string]any{"clientId": client.Id(), "subscriptions": []string{topic}})
		return f.request("POST", "/api/realtime", f.token, string(body)).Code
	}
	if status := subscribe(listed + "/*"); status != http.StatusNoContent {
		t.Errorf("listed subscription = %d", status)
	}
	for _, topic := range []string{"users/*", "private_notes/*", "_superusers/*?options=%7B%7D"} {
		if status := subscribe(topic); status != http.StatusForbidden {
			t.Errorf("subscription %s = %d, want 403", topic, status)
		}
	}
}

func TestPublicServingRefusesWeakPassword(t *testing.T) {
	publicServing.Store(true)
	t.Cleanup(func() { publicServing.Store(false) })
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	bindPublicGuards(app)
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	weak := core.NewRecord(users)
	weak.SetEmail("weak@example.invalid")
	weak.SetPassword("aaaaaaaa")
	if err := app.Save(weak); err == nil {
		t.Fatal("a weak password was accepted while serving publicly")
	}
	strong := core.NewRecord(users)
	strong.SetEmail("strong@example.invalid")
	strong.SetPassword("correct horse battery staple")
	if err := app.Save(strong); err != nil {
		t.Fatalf("a strong password was refused: %v", err)
	}
}
