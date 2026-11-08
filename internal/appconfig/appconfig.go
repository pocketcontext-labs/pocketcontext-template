// Package appconfig holds everything that makes this server TemplateContext
// rather than another application of pocketcontext-core: identity, public route
// allowlist, maintenance reads, copy and hooks. A derived product renames its
// identity here (template.json, tests/identity_check.py).
package appconfig

import (
	"path/filepath"
	"strings"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appidentity"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/maintenance"
	"github.com/pocketcontext-labs/pocketcontext-core/mcpobserve"
	"github.com/pocketcontext-labs/pocketcontext-core/profiles"

	assets "github.com/pocketcontext-labs/pocketcontext-template"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/buildinfo"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/server"
)

// Identity names this application everywhere. A derived product must set its
// own value for every field: tests/identity_check.py fails on any left unchanged.
var Identity = appidentity.Identity{
	AppID:             "templatecontext",
	DisplayName:       "TemplateContext",
	Command:           "templatecontext",
	SkillName:         "templatecontext",
	Marker:            ".templatecontext",
	RouterMarker:      ".templatecontext-router",
	RegistryNamespace: "templatecontext",
	CacheRoot:         "templatecontext",
	SocketHost:        "templatecontext.internal",
	SocketDirPrefix:   "templatecontext",
	DefaultPort:       8098,
	DatabaseFile:      "templatecontext.db",
	ConfigFile:        "templatecontext.json",
	EnvPrefix:         "TEMPLATECONTEXT",
	LocalKeyHeader:    "X-TemplateContext-Local-Key",
	LocalAccountEmail: "account@local.invalid", // persisted; nobody chooses or sees it, and .invalid never receives mail
	ReaderStorageKey:  "templatecontext.reader.auth",
	HookGlobal:        "$templatecontext",
}

// Text is the copy shown to people. The OAuth, OTP and budget strings are
// required by core but unreachable: this application starts no public listener.
var Text = appserver.Text{
	AccountName:        "TemplateContext",
	LegacyRecreateHint: "Use the release that created it, or start a new project with `templatecontext init --db NEW_PATH`",
	OAuthResourceName:  "TemplateContext",
	OAuthConsent: appserver.OAuthConsentText{
		ConnectionsHTML: `<p>This application has no remote access.</p>`,
		RequestHTML:     `<p><strong>{{.Name}}</strong> is asking for access, which this application does not grant.</p>`,
	},
	BudgetExhausted: "Request budget exhausted. Retry after the indicated delay.",
	OTPSubject:      "Log in to TemplateContext",
	OTPSenderName:   "TemplateContext",
}

// PublicRoute is the public route allowlist: empty. This application listens on
// loopback only and has no command that starts a public listener. Core's fixed
// checks run first; denying here makes the allowlist explicit if that changes.
func PublicRoute(core.App) func(method, path string) bool {
	return func(string, string) bool { return false }
}

// Policy returns the public routes, maintenance reads and observability lists.
func Policy() appserver.Policy {
	return appserver.Policy{
		PublicRoute: PublicRoute,
		Maintenance: maintenance.Policy{
			ReadPosts: map[string]bool{"/api/context/query": true, "/api/context/search": true, "/api/files/token": true},
			ReadGets:  map[string]bool{"/api/context/schema": true},
		},
		Observe: mcpobserve.Policy{
			Tools:     strings.Fields(""),
			APIRoutes: strings.Fields("/api/context/schema /api/context/query /api/context/search /api/collections/{collection}/records /api/collections/{collection}/records/{id}"),
		},
	}
}

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			return nil
		},
	}
}

// Runtime is the one validated runtime of this process.
var Runtime = mustRuntime()

// Profiles is this application's private profile registry.
var Profiles *profiles.Store = Runtime.Profiles()

func mustRuntime() *appserver.Runtime {
	rt, err := appserver.New(appserver.Config{
		Identity: Identity, Version: buildinfo.Version, Commit: buildinfo.Commit, Assets: assets.Assets,
		Policy: Policy(), Hooks: Hooks(), Text: Text,
	})
	if err != nil {
		panic(err)
	}
	return rt
}
