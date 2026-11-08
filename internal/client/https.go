package client

import (
	"net/mail"
	"net/url"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/appconfig"
)

// validateHTTPSArgs keeps public mode explicit: without --domain, serve stays on
// loopback exactly as before, and the public mode options are refused.
func validateHTTPSArgs(a *args) error {
	if a.bools["mcp"] && !a.has("domain") {
		return fail(2, "--mcp requires --domain and --headless")
	}
	if !a.has("domain") {
		if a.has("acme-email") {
			return fail(2, "--acme-email requires --domain")
		}
		if a.bools["headless"] {
			return fail(2, "--headless requires --domain")
		}
		if a.has("port") {
			return fail(2, "unknown option --port")
		}
		return nil
	}
	if err := appserver.ValidateHTTPSDomain(a.flags["domain"]); err != nil {
		return fail(2, "%v", err)
	}
	if !a.bools["headless"] || a.bools["tcp-only"] || a.has("port") || a.has("ui-dev") {
		return fail(2, "--domain requires --headless and cannot be combined with --tcp-only, --port or --ui-dev; HTTPS uses port 443 and HTTP uses port 80")
	}
	if a.has("acme-email") {
		email := a.flags["acme-email"]
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || parsed.Name != "" {
			return fail(2, "--acme-email must be a plain email address")
		}
	}
	return nil
}

// Public handoff is explicit and works from SSH. It must never start an ordinary
// background server in place of the user's supervised HTTPS service.
func openPublicReader(p *appserver.Project) (map[string]any, error) {
	info, err := appserver.Health(p)
	if err != nil {
		return nil, fail(1, "public HTTPS is not running; start the configured service before open --public: %v", err)
	}
	if err := appconfig.Runtime.CheckServerCompatibility(info); err != nil {
		return nil, err
	}
	if info.Key != p.Key || (p.Config.Instance != "" && info.Instance != p.Config.Instance) {
		return nil, fail(1, "running server identity differs from the selected project")
	}
	base := info.PublicURL
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || appserver.ValidateHTTPSDomain(u.Host) != nil {
		return nil, fail(1, "this server has no valid public HTTPS origin; start it with serve --headless --domain HOST")
	}
	data, err := appserver.LocalCall(p, "/api/bundle/login-code", map[string]any{"public": true})
	if err != nil {
		return nil, err
	}
	return publicReaderHandoff(base, data)
}

func publicReaderHandoff(base string, data map[string]any) (map[string]any, error) {
	link, _ := data["url"].(string)
	prefix := base + "/bundle/login#"
	code := strings.TrimPrefix(link, prefix)
	if !strings.HasPrefix(link, prefix) || len(code) != 48 || strings.Trim(code, "0123456789abcdef") != "" {
		return nil, fail(1, "server returned an invalid public sign-in link")
	}
	return map[string]any{
		"url": base, "signInLink": link, "expires": data["expires"], "public": true,
		"note": "Single-use sign-in link, valid for 2 minutes. Open it on your phone or another browser. This command does not verify public HTTPS reachability or browser sign-in.",
	}, nil
}

// ensureLocalTCP starts the server if needed and makes sure its loopback port is
// open. A running public service starts with only its private socket, and the
// local client, reader and tunnel all use the loopback port.
func ensureLocalTCP(p *appserver.Project, opts appserver.StartOptions) error {
	if err := appserver.EnsureRunning(p, opts); err != nil {
		return err
	}
	return appserver.EnableTCP(p)
}
