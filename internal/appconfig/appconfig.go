// Package appconfig holds everything that makes this server TemplateContext
// rather than another application of pocketcontext-core: identity, public route
// allowlist, maintenance reads, copy and hooks. A derived product renames its
// identity here (template.json, tests/identity_check.py).
package appconfig

import (
	"fmt"
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

// Text is the copy shown to people. The OAuth consent text is shown by public
// mode with --mcp; hosted mode (email sign-in, budgets) is not offered here.
var Text = appserver.Text{
	AccountName:        "TemplateContext",
	LegacyRecreateHint: "Use the release that created it, or start a new project with `templatecontext init --db NEW_PATH`",
	OAuthResourceName:  "TemplateContext",
	OAuthConsent: appserver.OAuthConsentText{
		ConnectionsHTML: `<p>These apps can read, search, query and change this application's records through its MCP tools. Revoke access to disconnect an app.</p>`,
		RequestHTML:     `<p><strong>{{.Name}}</strong> is asking to read, search, query and change (including delete) this application's records.</p><p>This does not grant server administration, shell or file access.</p>`,
	},
	BudgetExhausted: "Request budget exhausted. Retry after the indicated delay.",
	OTPSubject:      "Log in to TemplateContext",
	OTPSenderName:   "TemplateContext",
}

// Observe lists the diagnostic labels of the MCP tools and the routes they call.
var Observe = mcpobserve.Policy{
	Tools:     strings.Fields("read search schema query write"),
	APIRoutes: strings.Fields("/api/context/schema /api/context/query /api/context/search /api/collections/{collection}/records /api/collections/{collection}/records/{id}"),
}

// Policy returns the public routes, maintenance reads and observability lists.
func Policy() appserver.Policy {
	return appserver.Policy{
		PublicRoute: PublicRoute,
		Maintenance: maintenance.Policy{
			ReadPosts: map[string]bool{"/api/context/query": true, "/api/context/search": true, "/api/files/token": true},
			ReadGets:  map[string]bool{"/api/context/schema": true},
		},
		Observe: Observe,
	}
}

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		PreRegister: func(app core.App, _ *appserver.Project, opts appserver.ServeOptions) error {
			if opts.HTTPSDomain == "" {
				return nil
			}
			cfg, err := LoadPublicConfig()
			if err != nil {
				return fmt.Errorf("public mode refused: %w", err)
			}
			app.OnServe().BindFunc(func(e *core.ServeEvent) error {
				if err := checkPublicReady(e.App, cfg); err != nil {
					return fmt.Errorf("public mode refused: %w", err)
				}
				return e.Next()
			})
			return nil
		},
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			bindPublicGuards(app)
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
