package bundle

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// uiDevProxy serves only the reader through a separately managed Vite process.
// Application APIs, login links and protected files always stay on TemplateContext.
type uiDevProxy struct {
	proxy *httputil.ReverseProxy
	local *localRoutes
	csp   string
}

func newUIDevProxy(raw string, p *Project) (*uiDevProxy, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("--ui-dev must be http://127.0.0.1:PORT or http://localhost:PORT")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || port == p.Config.Port {
		return nil, errors.New("--ui-dev needs a port from 1 to 65535 different from the TemplateContext port")
	}
	// Pin localhost to a literal loopback address and never use an environment proxy.
	u.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	u.Path, u.RawPath = "", ""
	d := &uiDevProxy{
		local: &localRoutes{p: p},
		// Vite's reconnect probe uses a blob SharedWorker. This permission and
		// inline refresh scripts/styles apply only to development UI responses.
		csp: fmt.Sprintf("default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:; connect-src 'self' ws://127.0.0.1:%d ws://localhost:%d; img-src 'self'; media-src 'self' blob:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'", p.Config.Port, p.Config.Port),
	}
	d.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			// Do not give the asset server browser credentials, the local key,
			// forwarding headers or a Referer. Keep Origin so Vite also checks
			// its own WebSocket token after our same-origin check.
			headers := make(http.Header)
			for _, key := range []string{"Accept", "Origin", "Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
				if values := pr.Out.Header.Values(key); len(values) != 0 {
					headers[key] = values
				}
			}
			pr.Out.Header = headers
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			for key := range resp.Header {
				if strings.HasPrefix(strings.ToLower(key), "access-control-") {
					resp.Header.Del(key)
				}
			}
			d.headers(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			d.headers(w.Header())
			http.Error(w, "UI development server is unavailable. Start Vite and reload this page.", http.StatusBadGateway)
		},
	}
	return d, nil
}

func (d *uiDevProxy) headers(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", d.csp)
}

func (d *uiDevProxy) register(e *core.ServeEvent) {
	serve := func(re *core.RequestEvent) error {
		r := re.Request
		// Also check here so a future route/middleware refactor cannot expose
		// development sources or the WebSocket under an unexpected Host.
		if !d.local.allowedHost(r.Host) {
			return re.ForbiddenError("Unexpected host.", nil)
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.Host {
			return re.ForbiddenError("Unexpected UI origin.", nil)
		}
		upgrade := r.Header.Get("Upgrade") != ""
		if r.URL.Path == "/__ui_hmr" {
			if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || origin == "" {
				return re.ForbiddenError("UI hot reload requires a same-origin WebSocket.", nil)
			}
		} else if upgrade {
			return re.ForbiddenError("Unexpected WebSocket path.", nil)
		}
		d.proxy.ServeHTTP(re.Response, r)
		return nil
	}
	// GET includes HEAD in Go's router. Deliberately omit a catch-all and /api,
	// /bundle and /_: Vite must never turn an API error into an HTML response.
	for _, path := range []string{"/{$}", "/index.html", "/src/{path...}", "/assets/{path...}", "/node_modules/{path...}", "/@vite/{path...}", "/@id/{path...}", "/@fs/{path...}", "/@react-refresh", "/__ui_hmr"} {
		e.Router.GET(path, serve)
	}
}
