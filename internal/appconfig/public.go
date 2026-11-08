package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/router"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	assets "github.com/pocketcontext-labs/pocketcontext-template"
)

// PublicPasswordField is the hidden flag on the account that records whether its
// password met the public mode rule when it was saved.
const PublicPasswordField = "publicPasswordStrong"

// Public mode needs a password that can face the internet: at least this many
// characters, and at least minPublicPasswordDistinct different ones.
const (
	minPublicPasswordLength   = 12
	minPublicPasswordDistinct = 5
)

// publicPasswordStrong reports whether a plain password meets the public mode rule.
func publicPasswordStrong(password string) bool {
	if utf8.RuneCountInString(password) < minPublicPasswordLength {
		return false
	}
	distinct := map[rune]bool{}
	for _, r := range password {
		distinct[r] = true
	}
	return len(distinct) >= minPublicPasswordDistinct
}

// PublicConfig is the public mode section of server.json, validated against the
// rest of that file. It is the single allowlist of what the public origin
// exposes, so a derived product changes data, not Go.
type PublicConfig struct {
	Collections    []string
	Columns        map[string][]string
	Indexes        []string
	authCollection string
}

var publicCollectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]*$`)

// LoadPublicConfig reads the embedded server.json.
func LoadPublicConfig() (*PublicConfig, error) {
	data, err := fs.ReadFile(assets.Assets, "server.json")
	if err != nil {
		return nil, err
	}
	return ParsePublicConfig(data)
}

// ParsePublicConfig validates the public section of a server.json document.
func ParsePublicConfig(data []byte) (*PublicConfig, error) {
	var raw struct {
		AuthCollection string              `json:"authCollection"`
		Tables         map[string][]string `json:"tables"`
		Public         *struct {
			Collections []string `json:"collections"`
		} `json:"public"`
		Search *struct {
			Indexes map[string]json.RawMessage `json:"indexes"`
		} `json:"search"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("server.json is invalid: %w", err)
	}
	if raw.Public == nil || len(raw.Public.Collections) == 0 {
		return nil, errors.New(`server.json has no "public" section listing collections`)
	}
	if raw.AuthCollection == "" {
		return nil, errors.New("server.json has no authCollection")
	}
	cfg := &PublicConfig{Columns: map[string][]string{}, authCollection: raw.AuthCollection}
	for _, name := range raw.Public.Collections {
		switch {
		case !publicCollectionName.MatchString(name):
			return nil, fmt.Errorf("public collection %q is a system collection or an invalid name", name)
		case strings.EqualFold(name, raw.AuthCollection):
			return nil, fmt.Errorf("public collection %q is the auth collection", name)
		case raw.Tables[name] == nil:
			return nil, fmt.Errorf("public collection %q is not in tables", name)
		case cfg.Columns[name] != nil:
			return nil, fmt.Errorf("public collection %q is listed twice", name)
		}
		cfg.Collections = append(cfg.Collections, name)
		cfg.Columns[name] = raw.Tables[name]
	}
	// SQL and search read every table in `tables`, on the public origin too.
	for name := range raw.Tables {
		if cfg.Columns[name] == nil {
			return nil, fmt.Errorf("table %q is readable through SQL but is not in public.collections; list it or remove it from tables", name)
		}
	}
	if raw.Search != nil {
		for name := range raw.Search.Indexes {
			cfg.Indexes = append(cfg.Indexes, name)
		}
		slices.Sort(cfg.Indexes)
	}
	return cfg, nil
}

// Listed reports whether a collection is on the public allowlist.
func (c *PublicConfig) Listed(name string) bool { return c.Columns[name] != nil }

// Route is the public route allowlist: the reader and its assets, password and
// one-time-code sign-in, the authenticated SQL and search APIs, realtime, and
// record routes of the listed collections. Anything else is denied, including
// any local API added later. Core adds the OAuth and MCP paths itself, and only
// with --mcp.
func (c *PublicConfig) Route(method, path string) bool {
	read := method == http.MethodGet || method == http.MethodHead
	if read {
		switch path {
		case "/", "/bundle/login", "/bundle/login.js", "/api/bundle/auth-mode", "/api/context/schema", "/api/realtime":
			return true
		}
		if strings.HasPrefix(path, "/assets/") && appserver.ReaderAssetName(strings.TrimPrefix(path, "/assets/")) {
			return true
		}
	}
	if method == http.MethodPost {
		switch path {
		case "/api/bundle/redeem", "/api/collections/users/auth-with-password", "/api/collections/users/auth-refresh",
			"/api/context/query", "/api/context/search", "/api/realtime":
			return true
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "collections" || parts[3] != "records" || !c.Listed(parts[2]) {
		return false
	}
	switch len(parts) {
	case 4:
		return read || method == http.MethodPost
	case 5:
		return parts[4] != "" && (read || method == http.MethodPatch || method == http.MethodDelete)
	}
	return false
}

// PublicRoute is the public route allowlist of Policy, built from server.json.
// A missing or invalid configuration denies everything.
func PublicRoute(core.App) func(method, path string) bool {
	return publicRoute(LoadPublicConfig())
}

func publicRoute(cfg *PublicConfig, err error) func(method, path string) bool {
	if err != nil || cfg == nil {
		return func(string, string) bool { return false }
	}
	return cfg.Route
}

// checkPublicReady refuses to start public mode for a configuration or account
// that is not fit for it. It runs after migrations and before any listener.
func checkPublicReady(app core.App, cfg *PublicConfig) error {
	for _, name := range cfg.Collections {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			return fmt.Errorf("public collection %q does not exist", name)
		}
		if collection.System || collection.Type != core.CollectionTypeBase {
			return fmt.Errorf("public collection %q must be an ordinary base collection", name)
		}
	}
	accounts, err := app.FindRecordsByFilter(cfg.authCollection, "", "", 2, 0)
	if err != nil {
		return err
	}
	if len(accounts) != 1 {
		return fmt.Errorf("public mode needs the project's account; run `%s init` first", Identity.Command)
	}
	if !accounts[0].GetBool(PublicPasswordField) {
		return fmt.Errorf("public mode needs a stronger account password (at least %d characters, %d of them different), and one set by an earlier release cannot be checked; run `%s password`, then start again",
			minPublicPasswordLength, minPublicPasswordDistinct, Identity.Command)
	}
	return nil
}

// publicServing is set before a public server starts, so a running service
// cannot be left with a password its next start would refuse.
var publicServing atomic.Bool

// MarkPublicServing makes this process refuse weak account passwords.
func MarkPublicServing() { publicServing.Store(true) }

// bindPublicGuards records password strength whenever a password is saved, and
// keeps realtime subscriptions from the public origin on the allowlist.
func bindPublicGuards(app core.App) {
	password := func(e *core.RecordEvent) error {
		// Empty unless this save sets a password: loaded records hold only a hash.
		if plain := e.Record.GetString("password"); plain != "" {
			strong := publicPasswordStrong(plain)
			if !strong && publicServing.Load() {
				return router.NewBadRequestError(fmt.Sprintf("Public mode needs a password of at least %d characters, %d of them different.", minPublicPasswordLength, minPublicPasswordDistinct), nil)
			}
			e.Record.Set(PublicPasswordField, strong)
		}
		return e.Next()
	}
	app.OnRecordCreate("users").BindFunc(password)
	app.OnRecordUpdate("users").BindFunc(password)
	app.OnRealtimeSubscribeRequest().BindFunc(func(e *core.RealtimeSubscribeRequestEvent) error {
		if appserver.IsPublicRequest(e.Request) {
			cfg, err := LoadPublicConfig()
			for _, subscription := range e.Subscriptions {
				if err != nil || !cfg.Listed(subscriptionCollection(subscription)) {
					return e.ForbiddenError("This subscription is not available on the public origin.", nil)
				}
			}
		}
		return e.Next()
	})
}

// subscriptionCollection returns the collection of "name/id" or "name/*?options=...".
func subscriptionCollection(subscription string) string {
	name, _, _ := strings.Cut(subscription, "?")
	name, _, _ = strings.Cut(name, "/")
	return name
}

// ValidatedMCPPublicRequest identifies a request that passed the configured
// public host and origin boundary. A client or forwarded header cannot supply it.
func ValidatedMCPPublicRequest(r *http.Request) bool {
	origin, ok := appserver.PublicOrigin(r)
	if !ok || !appserver.IsPublicRequest(r) {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || strings.TrimSuffix(r.Host, ":443") != u.Host {
		return false
	}
	return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == origin
}
