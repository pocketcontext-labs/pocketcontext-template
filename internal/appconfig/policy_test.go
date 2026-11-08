package appconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/jsvm"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver/routepolicytest"
)

// TestPublicRouteDeniesEverything: there is no public listener, and the policy
// passed to core is an explicit deny for the whole frozen route table, including
// the routes a loopback client may reach anonymously, and core's negative set.
func TestPublicRouteDeniesEverything(t *testing.T) {
	route := PublicRoute(nil)
	routepolicytest.Check(t, route, routepolicytest.CommonDenied())
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "testdata", "public_routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(data, &table); err != nil || len(table) < 300 {
		t.Fatalf("table: %d rows, %v", len(table), err)
	}
	for _, row := range table {
		if route(row.Method, row.Path) {
			t.Errorf("%s %s is public", row.Method, row.Path)
		}
	}
}

// TestPublicHandlerDeniesEverything drives the real public handler of core
// with this application's policy: nothing but core's fixed routes answers.
func TestPublicHandlerDeniesEverything(t *testing.T) {
	app := newHookApp(t)
	server, err := Runtime.NewTestServer(app, appserver.TestServerOptions{Domain: "template.example.test", Cache: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/bundle/login", "/api/health", "/api/bundle/instance", "/api/collections/greetings/records"} {
		status := publicStatus(server, path)
		if status != 404 {
			t.Errorf("public GET %s = %d, want 404", path, status)
		}
	}
}

// TestRealHooksRefuseSecondAccount: account_access.js allows the one account and
// refuses a second one or its deletion, whatever saved it.
func TestRealHooksRefuseSecondAccount(t *testing.T) {
	app := newHookApp(t)
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	account := func(email string) *core.Record {
		record := core.NewRecord(users)
		record.SetEmail(email)
		record.SetPassword("synthetic-password")
		return record
	}
	first := account(Identity.LocalAccountEmail)
	if err := app.Save(first); err != nil {
		t.Fatal(err)
	}
	err = app.Save(account("second@example.test"))
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("second account: %v", err)
	}
	if err := app.Delete(first); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleting the account: %v", err)
	}
	count, err := app.CountRecords("users")
	if err != nil || count != 1 {
		t.Fatalf("accounts = %d, %v", count, err)
	}
}

func newHookApp(t *testing.T) core.App {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	jsvm.MustRegister(app, Runtime.HooksConfig(root, ""))
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}
