package appconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/jsvm"
)

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
