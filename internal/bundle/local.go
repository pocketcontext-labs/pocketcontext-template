package bundle

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// Local routes provide sign-in without an identity provider. Reading the private key file proves the
// caller is this computer's user, who can already read the database file.
// Every other API still requires an ordinary users token.

// KeyHeader carries the private local key to the local-only routes.
const KeyHeader = "X-TemplateContext-Local-Key"

type localRoutes struct {
	p     *Project
	store *Store
	key   string
	stop  chan struct{}
	codes codeStore
	uiDev string
}

// Sign-in code lifetimes in seconds. Codes live only in server memory.
const (
	MinCodeTTL     = 30
	MaxCodeTTL     = 600
	DefaultCodeTTL = 120
)

// ValidCodeTTL reports whether seconds is an allowed sign-in code lifetime.
func ValidCodeTTL(seconds int) bool { return seconds >= MinCodeTTL && seconds <= MaxCodeTTL }

type loginCode struct {
	user    string
	expires time.Time
}

// codeStore holds single-use sign-in codes in memory.
type codeStore struct {
	mu    sync.Mutex
	codes map[string]loginCode
	now   func() time.Time
}

func (s *codeStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// issue creates a code for the user and purges expired ones.
func (s *codeStore) issue(user string, ttl time.Duration) (string, time.Time) {
	code := RandomHex(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.codes == nil {
		s.codes = map[string]loginCode{}
	}
	for c, v := range s.codes {
		if now.After(v.expires) {
			delete(s.codes, c)
		}
	}
	expires := now.Add(ttl)
	s.codes[code] = loginCode{user, expires}
	return code, expires
}

// take consumes a code and returns its user when it was valid.
func (s *codeStore) take(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.codes[code]
	delete(s.codes, code) // single use
	if !ok || s.clock().After(v.expires) {
		return "", false
	}
	return v.user, true
}

func loopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (l *localRoutes) allowedHost(host string) bool {
	port := strings.TrimPrefix(l.p.URL(), "http://127.0.0.1")
	return host == "127.0.0.1"+port || host == "localhost"+port
}

func (l *localRoutes) requireKey(e *core.RequestEvent) error {
	given := e.Request.Header.Get(KeyHeader)
	if !loopback(e.Request) || subtle.ConstantTimeCompare([]byte(given), []byte(l.key)) != 1 {
		return e.ForbiddenError("A local key is required.", nil)
	}
	return nil
}

// account returns the configured account, or the only enabled one.
func (l *localRoutes) account(app core.App) (*core.Record, error) {
	if l.p.Config.Email != "" {
		return app.FindAuthRecordByEmail("users", l.p.Config.Email)
	}
	records, err := app.FindRecordsByFilter("users", "disabled = false", "created", 2, 0)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, apis.NewBadRequestError("Set the account email in templatecontext.json.", nil)
	}
	return records[0], nil
}

func (l *localRoutes) register(e *core.ServeEvent) {
	// DNS rebinding protection: a web page cannot reach this server under another name.
	e.Router.Bind(&hook.Handler[*core.RequestEvent]{
		Id: "templatecontextLocalHost", Priority: -99999,
		Func: func(re *core.RequestEvent) error {
			if !l.allowedHost(re.Request.Host) {
				return re.ForbiddenError("Unexpected host.", nil)
			}
			return re.Next()
		},
	})

	e.Router.GET("/api/bundle/instance", func(re *core.RequestEvent) error {
		info := map[string]any{
			"app": "templatecontext", "version": Version, "instance": l.p.Config.Instance, "key": l.p.Key,
		}
		if l.uiDev != "" {
			// The repository launcher verifies the exact child before it may
			// stop it; an existing server on this port is never adopted.
			info["uiDev"], info["pid"] = l.uiDev, os.Getpid()
		}
		return re.JSON(http.StatusOK, info)
	})

	e.Router.POST("/api/bundle/token", func(re *core.RequestEvent) error {
		if err := l.requireKey(re); err != nil {
			return err
		}
		user, err := l.account(re.App)
		if err != nil {
			return re.BadRequestError("No local account; run `templatecontext init`.", nil)
		}
		return apis.RecordAuthResponse(re, user, "local", nil)
	})

	e.Router.POST("/api/bundle/account", func(re *core.RequestEvent) error {
		if err := l.requireKey(re); err != nil {
			return err
		}
		var body struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("Invalid account.", nil)
		}
		if body.Password != "" && len(body.Password) < 8 {
			return re.BadRequestError("The password needs at least 8 characters.", nil)
		}
		user, err := re.App.FindAuthRecordByEmail("users", body.Email)
		created := err != nil
		if created {
			if body.Password == "" {
				// Sign-in happens with one-time codes; nobody learns this password.
				body.Password = RandomHex(32)
			}
			users, err := re.App.FindCollectionByNameOrId("users")
			if err != nil {
				return err
			}
			user = core.NewRecord(users)
			user.SetEmail(body.Email)
			user.SetVerified(true)
			name := strings.TrimSpace(body.Name)
			if name == "" {
				name = strings.Split(body.Email, "@")[0]
			}
			user.Set("name", name)
		}
		if body.Password != "" {
			user.SetPassword(body.Password)
		}
		if err := re.App.Save(user); err != nil {
			return re.BadRequestError("Could not save the account: "+err.Error(), nil)
		}
		return re.JSON(http.StatusOK, map[string]any{"id": user.Id, "email": user.Email(), "created": created})
	})

	e.Router.POST("/api/bundle/login-code", func(re *core.RequestEvent) error {
		if err := l.requireKey(re); err != nil {
			return err
		}
		var body struct {
			Email string `json:"email"`
			TTL   *int   `json:"ttl"`
		}
		if re.Request.ContentLength != 0 {
			if err := re.BindBody(&body); err != nil {
				return re.BadRequestError("Invalid request.", nil)
			}
		}
		ttl := DefaultCodeTTL
		if body.TTL != nil {
			ttl = *body.TTL
		}
		if !ValidCodeTTL(ttl) {
			return re.BadRequestError(fmt.Sprintf("ttl must be %d-%d seconds.", MinCodeTTL, MaxCodeTTL), nil)
		}
		var user *core.Record
		var err error
		if email := strings.TrimSpace(body.Email); email != "" {
			user, err = re.App.FindAuthRecordByEmail("users", strings.ToLower(email))
			if err != nil || user.GetBool("disabled") {
				return re.BadRequestError("No active account with that email; create it with `templatecontext account`.", nil)
			}
		} else if user, err = l.account(re.App); err != nil {
			return re.BadRequestError("No local account; run `templatecontext init`.", nil)
		}
		code, expires := l.codes.issue(user.Id, time.Duration(ttl)*time.Second)
		return re.JSON(http.StatusOK, map[string]string{
			"url":     l.p.URL() + "/bundle/login#" + code,
			"code":    code,
			"email":   user.Email(),
			"expires": expires.UTC().Format(time.RFC3339),
		})
	})

	e.Router.POST("/api/bundle/redeem", func(re *core.RequestEvent) error {
		var body struct {
			Code string `json:"code"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("Invalid code.", nil)
		}
		id, ok := l.codes.take(body.Code)
		if !ok {
			return re.UnauthorizedError("This sign-in code is used or expired.", nil)
		}
		user, err := re.App.FindRecordById("users", id)
		if err != nil {
			return re.UnauthorizedError("This sign-in code is used or expired: the account no longer exists.", nil)
		}
		if user.GetBool("disabled") {
			return re.ForbiddenError("This account is disabled.", nil)
		}
		return apis.RecordAuthResponse(re, user, "local", nil)
	})

	e.Router.POST("/api/bundle/flush", func(re *core.RequestEvent) error {
		if err := l.requireKey(re); err != nil {
			return err
		}
		l.store.mu.Lock()
		err := l.store.writeBack()
		l.store.mu.Unlock()
		if err != nil {
			return re.InternalServerError("Write-back failed: "+err.Error(), nil)
		}
		return re.JSON(http.StatusOK, map[string]string{"db": l.p.DB})
	})

	e.Router.POST("/api/bundle/stop", func(re *core.RequestEvent) error {
		if err := l.requireKey(re); err != nil {
			return err
		}
		select {
		case l.stop <- struct{}{}:
		default:
		}
		return re.JSON(http.StatusOK, map[string]bool{"stopping": true})
	})

	page := func(re *core.RequestEvent, contentType, body string) error {
		h := re.Response.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		return re.Blob(http.StatusOK, contentType, []byte(body))
	}
	e.Router.GET("/bundle/login", func(re *core.RequestEvent) error {
		return page(re, "text/html; charset=utf-8", loginHTML)
	})
	e.Router.GET("/bundle/login.js", func(re *core.RequestEvent) error {
		return page(re, "text/javascript; charset=utf-8", loginJS)
	})
}

const loginHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>TemplateContext sign-in</title></head>
<body><p id="status">Signing in…</p><script src="/bundle/login.js"></script></body></html>`

// The code travels in the fragment, so it never reaches logs or the Referer.
const loginJS = `(async () => {
  const status = document.getElementById("status");
  const code = location.hash.slice(1);
  history.replaceState(null, "", "/bundle/login");
  try {
    const response = await fetch("/api/bundle/redeem", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({code})});
    const data = await response.json();
    if (!response.ok) throw new Error(data.message || "Sign-in failed.");
    localStorage.setItem("templatecontext.reader.auth", JSON.stringify({token: data.token, record: data.record}));
    location.replace("/");
  } catch (error) {
    status.textContent = error.message + " Get a new link with templatecontext open, or sign in with your password.";
  }
})();`
