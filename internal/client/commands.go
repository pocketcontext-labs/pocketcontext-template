package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/bundle"
)

// args is a small argparse-like parser: flags may appear anywhere.
type args struct {
	pos   []string
	flags map[string]string
	bools map[string]bool
}

// parseArgs accepts the given value flags and boolean flags (without dashes).
func parseArgs(argv []string, values, booleans []string) (*args, error) {
	a := &args{flags: map[string]string{}, bools: map[string]bool{}}
	isValue, isBool := map[string]bool{}, map[string]bool{}
	for _, v := range append(values, "db", "url") {
		isValue[v] = true
	}
	for _, b := range append(booleans, "pretty") {
		isBool[b] = true
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			a.pos = append(a.pos, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") || arg == "-" {
			a.pos = append(a.pos, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		switch {
		case isBool[name] && !hasValue:
			a.bools[name] = true
		case isValue[name]:
			if !hasValue {
				if i+1 >= len(argv) {
					return nil, fail(2, "--%s needs a value", name)
				}
				i++
				value = argv[i]
			}
			a.flags[name] = value
		default:
			return nil, fail(2, "unknown option --%s", name)
		}
	}
	return a, nil
}

func (a *args) has(name string) bool { _, ok := a.flags[name]; return ok }

func (a *args) need(n int, usage string) error {
	if len(a.pos) != n {
		return fail(2, "usage: templatecontext %s", usage)
	}
	return nil
}

func (a *args) intFlag(name string, fallback int) (int, error) {
	v, ok := a.flags[name]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fail(2, "--%s must be a whole number", name)
	}
	return n, nil
}

func readArg(text, what string) ([]byte, error) {
	if text == "-" {
		data, err := io.ReadAll(os.Stdin)
		return data, err
	}
	if text == "" {
		return nil, fail(2, "%s is empty", what)
	}
	return []byte(text), nil
}

func readPassword(fromStdin bool, confirm bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fail(2, "no terminal to ask for a password; pass --password-stdin and write the password to standard input")
	}
	fmt.Fprint(os.Stderr, "Password (at least 8 characters): ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Repeat the password: ")
		second, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", fail(2, "the passwords differ")
		}
	}
	return string(first), nil
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// Run executes one command and returns the process exit code.
func Run(argv []string) int {
	if err := run(argv); err != nil {
		var f *Fail
		if errors.As(err, &f) {
			fmt.Fprintln(os.Stderr, "templatecontext: "+f.Message)
			return f.Code
		}
		fmt.Fprintln(os.Stderr, "templatecontext: "+err.Error())
		return 1
	}
	return 0
}

func run(argv []string) error {
	// Global options may precede the command: templatecontext --db PATH COMMAND ...
	var global []string
	for len(argv) > 0 && strings.HasPrefix(argv[0], "--") {
		name, _, hasValue := strings.Cut(argv[0][2:], "=")
		switch {
		case name == "pretty":
			global, argv = append(global, argv[0]), argv[1:]
		case (name == "db" || name == "url") && hasValue:
			global, argv = append(global, argv[0]), argv[1:]
		case (name == "db" || name == "url") && len(argv) > 1:
			global, argv = append(global, argv[0], argv[1]), argv[2:]
		default:
			goto parsed
		}
	}
parsed:
	if len(argv) > 0 {
		argv = append([]string{argv[0]}, append(global, argv[1:]...)...)
	}
	if len(argv) == 0 || argv[0] == "--help" || argv[0] == "-h" {
		return printHelp("")
	}
	command, rest := argv[0], argv[1:]
	// Help comes before argument parsing, so `COMMAND --help` works for every command.
	if command == "help" {
		if wantsHelp(rest) {
			return printHelp("help")
		}
		topic, err := helpTopic(rest)
		if err != nil {
			return err
		}
		return printHelp(topic)
	}
	if wantsHelp(rest) {
		if _, known := commandHelp[command]; known {
			return printHelp(command)
		}
	}
	if command == "version" || command == "--version" {
		line := "templatecontext " + bundle.Version
		if bundle.Commit != "" {
			line += " (" + bundle.Commit + ")"
		}
		fmt.Println(line)
		return nil
	}
	spec := map[string][2][]string{
		"serve":          {{"ui-dev"}, {"take-over"}},
		"start":          {nil, {"take-over"}},
		"ensure-running": {nil, {"take-over"}},
		"stop":           {nil, nil},
		"status":         {nil, nil},
		"open":           {{"email"}, {"no-browser"}},
		"flush":          {nil, nil},
		"recover":        {{"keep"}, nil},
		"init":           {{"email", "name", "port"}, {"password-stdin"}},
		"password":       {nil, {"password-stdin"}},
		"skill":          {{"agent", "dir"}, nil},
		"whoami":         {nil, nil},
		"schema":         {nil, nil},
		"sql":            {nil, nil},
		"query":          {nil, nil},
		"add":            {nil, nil},
		"list":           {{"limit"}, nil},
		"search":         {{"limit", "offset", "generation"}, nil},
		"account":        {{"email", "name"}, {"password-stdin"}},
		"login-code":     {{"email", "ttl"}, nil},
		"login":          {{"email"}, {"password-stdin", "code-stdin"}},
		"logout":         {nil, nil},
		"tunnel":         {nil, {"new-address", "address-stdin"}},
	}
	s, known := spec[command]
	if !known {
		return fail(2, "unknown command %q; run `templatecontext help`", command)
	}
	a, err := parseArgs(rest, s[0], s[1])
	if err != nil {
		return err
	}
	pretty := a.bools["pretty"]
	// The tunnel carries the port between computers; it is neither a server
	// nor a data command, so it chooses its own side below.
	if command == "tunnel" {
		return runTunnel(a, pretty)
	}
	// --url, or TEMPLATECONTEXT_URL without an explicit --db, selects remote mode.
	remote, fromFlag := a.flags["url"], a.has("url")
	if fromFlag && a.has("db") {
		return fail(2, "choose --url or --db, not both")
	}
	if !fromFlag && !a.has("db") {
		remote = os.Getenv("TEMPLATECONTEXT_URL")
	}
	if fromFlag || remote != "" {
		base, err := normalizeURL(remote)
		if err != nil {
			return err
		}
		return runRemote(base, command, a, pretty)
	}
	if command == "login" || command == "logout" {
		return fail(2, "usage: templatecontext %s --url http://127.0.0.1:PORT", command)
	}
	p, err := bundle.Resolve(a.flags["db"])
	if err != nil {
		return fail(2, "%v", err)
	}
	switch command {
	case "serve":
		if a.has("ui-dev") && a.flags["ui-dev"] == "" {
			return fail(2, "--ui-dev needs a loopback Vite URL")
		}
		return bundle.Serve(p, bundle.ServeOptions{TakeOver: a.bools["take-over"], UIDev: a.flags["ui-dev"]})
	case "start", "ensure-running":
		if err := bundle.EnsureRunning(p, bundle.StartOptions{TakeOver: a.bools["take-over"]}); err != nil {
			return fail(1, "%v", err)
		}
		Print(map[string]any{"status": "running", "url": p.URL(), "db": p.DB, "instance": p.Config.Instance}, pretty)
		return nil
	case "stop":
		if err := bundle.Stop(p); err != nil {
			return fail(1, "%v", err)
		}
		Print(map[string]any{"status": "stopped", "db": p.DB}, pretty)
		return nil
	case "status":
		running, err := bundle.Running(p)
		lease, _ := bundle.ReadLease(p)
		out := map[string]any{"db": p.DB, "url": p.URL(), "running": running, "config": p.ConfigPath, "storage": p.StorageDir(),
			"cache": p.Cache, "log": p.LogFile(), "lease": lease, "conflicts": bundle.Conflicts(p), "version": bundle.Version}
		if err != nil {
			out["error"] = err.Error()
		}
		Print(out, pretty)
		return nil
	case "flush":
		if err := bundle.EnsureRunning(p, bundle.StartOptions{}); err != nil {
			return fail(1, "%v", err)
		}
		data, err := bundle.LocalCall(p, "/api/bundle/flush", map[string]any{})
		if err != nil {
			return fail(1, "%v", err)
		}
		Print(data, pretty)
		return nil
	case "recover":
		keep := a.flags["keep"]
		if keep != "local" && keep != "synced" {
			return fail(2, "usage: templatecontext recover --keep local|synced")
		}
		if err := bundle.Recover(p, keep == "local"); err != nil {
			return fail(1, "%v", err)
		}
		Print(map[string]any{"recovered": keep, "db": p.DB}, pretty)
		return nil
	case "init":
		return initProject(p, a, pretty)
	case "password":
		password, err := readPassword(a.bools["password-stdin"], true)
		if err != nil {
			return err
		}
		c, err := New(p)
		if err != nil {
			return err
		}
		me, err := c.Must("POST", "/api/collections/users/auth-refresh", nil)
		if err != nil {
			return err
		}
		email, _ := dig(me, "record", "email").(string)
		if _, err := bundle.LocalCall(p, "/api/bundle/account", map[string]string{"email": email, "password": password}); err != nil {
			return fail(1, "%v", err)
		}
		Print(map[string]any{"email": email, "password": "changed"}, pretty)
		return nil
	case "open":
		if err := bundle.EnsureRunning(p, bundle.StartOptions{}); err != nil {
			return fail(1, "%v", err)
		}
		body := map[string]any{}
		if a.has("email") {
			email := strings.ToLower(a.flags["email"])
			if !emailPattern.MatchString(email) {
				return fail(2, "usage: templatecontext open [--email EMAIL] [--no-browser]")
			}
			body["email"] = email
		}
		data, err := bundle.LocalCall(p, "/api/bundle/login-code", body)
		if err != nil {
			return fail(1, "%v", err)
		}
		link, _ := data["url"].(string)
		if !a.bools["no-browser"] {
			if err := bundle.OpenBrowser(link); err != nil {
				fmt.Fprintln(os.Stderr, "could not open a browser:", err)
			}
		}
		Print(map[string]any{"url": p.URL(), "signInLink": link, "note": "single-use link, valid for 2 minutes"}, pretty)
		return nil
	case "account":
		return createAccount(p, a, pretty)
	case "login-code":
		return loginCode(p, a, pretty)
	case "skill":
		return installSkills(p.DB, p.URL(), a.flags["agent"], a.flags["dir"], pretty)
	}
	c, err := New(p)
	if err != nil {
		return err
	}
	return runData(c, command, a, pretty)
}

// localOnly are the server and project commands that need the local database.
var localOnly = map[string]bool{"serve": true, "start": true, "ensure-running": true, "stop": true, "flush": true,
	"recover": true, "init": true, "password": true, "account": true, "login-code": true}

// runRemote runs a command against a forwarded server. It never resolves a
// database or starts a server.
func runRemote(base, command string, a *args, pretty bool) error {
	if localOnly[command] {
		return fail(2, "%s is not available with --url: the server and its database live on the agent computer; run it there", command)
	}
	switch command {
	case "logout":
		if err := deleteSession(base); err != nil {
			return err
		}
		Print(map[string]any{"url": base, "loggedOut": true}, pretty)
		return nil
	case "status":
		out, err := remoteStatus(base)
		if err != nil {
			return err
		}
		Print(out, pretty)
		return nil
	case "login":
		var out map[string]any
		var err error
		if a.bools["code-stdin"] {
			if a.has("email") || a.bools["password-stdin"] {
				return fail(2, "usage: templatecontext login --url URL --code-stdin (the code names the account; no --email or --password-stdin)")
			}
			out, err = loginWithCode(base)
		} else {
			out, err = login(base, a.flags["email"], a.bools["password-stdin"])
		}
		if err != nil {
			return err
		}
		Print(out, pretty)
		return nil
	}
	if command == "open" && a.has("email") {
		return fail(2, "open --email is not available with --url: run `templatecontext open --email EMAIL --no-browser` on the agent computer and open the printed link here")
	}
	s, err := loadSession(base)
	if err != nil {
		return err
	}
	if _, err := checkRemote(base, s); err != nil {
		return err
	}
	switch command {
	case "open":
		if !a.bools["no-browser"] {
			if err := bundle.OpenBrowser(base); err != nil {
				fmt.Fprintln(os.Stderr, "could not open a browser:", err)
			}
		}
		Print(map[string]any{"url": base, "note": "sign in with your email and password, or open a sign-in link from `templatecontext open --email EMAIL --no-browser` on the agent computer"}, pretty)
		return nil
	case "skill":
		return installSkills("", base, a.flags["agent"], a.flags["dir"], pretty)
	}
	c, err := NewRemote(base)
	if err != nil {
		return err
	}
	return runData(c, command, a, pretty)
}

// runData runs the data commands, which work locally and remotely.
func runData(c *Client, command string, a *args, pretty bool) error {
	var data any
	var err error
	switch command {
	case "whoami":
		me, err := c.Must("POST", "/api/collections/users/auth-refresh", nil)
		if err != nil {
			return err
		}
		out := map[string]any{"id": dig(me, "record", "id"), "name": dig(me, "record", "name"), "email": dig(me, "record", "email"), "url": c.Base}
		if c.DB != "" {
			out["db"] = c.DB
		}
		data = out
	case "schema":
		data, err = c.Must("GET", "/api/context/schema", nil)
	case "query", "sql":
		if err := a.need(1, command+" SQL|-"); err != nil {
			return err
		}
		sql, err := readArg(a.pos[0], "the query")
		if err != nil {
			return err
		}
		result, err := c.Query(string(sql))
		if err != nil {
			return err
		}
		if t, _ := result["truncated"].(bool); t {
			rows, _ := result["rows"].([]any)
			fmt.Fprintf(os.Stderr, "WARNING: result truncated to %d rows by the server's row or byte limit. Select fewer columns, narrow the query, or page with ORDER BY and LIMIT/OFFSET.\n", len(rows))
		}
		data = result
	case "add":
		if err := a.need(1, "add MESSAGE|-"); err != nil {
			return err
		}
		message, err := readArg(a.pos[0], "the message")
		if err != nil {
			return err
		}
		data, err = c.AddGreeting(string(message))
		if err != nil {
			return err
		}
	case "list":
		if err := a.need(0, "list [--limit N]"); err != nil {
			return err
		}
		limit, err := a.intFlag("limit", 50)
		if err != nil {
			return err
		}
		data, err = c.ListGreetings(limit)
		if err != nil {
			return err
		}
	case "search":
		if err := a.need(1, "search TERMS [--limit N] [--offset N --generation G]"); err != nil {
			return err
		}
		o := SearchOptions{Generation: a.flags["generation"]}
		if o.Limit, err = a.intFlag("limit", 20); err != nil {
			return err
		}
		if o.Offset, err = a.intFlag("offset", 0); err != nil {
			return err
		}
		data, err = c.Search(a.pos[0], o)
	}
	if err != nil {
		return err
	}
	Print(data, pretty)
	return nil
}

// createAccount creates another account on this server, or resets its
// password, so that a person working remotely signs in as themselves.
func createAccount(p *bundle.Project, a *args, pretty bool) error {
	email := strings.ToLower(a.flags["email"])
	if !emailPattern.MatchString(email) {
		return fail(2, "usage: templatecontext account --email EMAIL [--name NAME] [--password-stdin]")
	}
	// Without --password-stdin no password is sent: a new account gets a random
	// one nobody knows (sign in with `login-code`), an existing one keeps its own.
	password := ""
	if a.bools["password-stdin"] {
		var err error
		if password, err = readPassword(true, false); err != nil {
			return err
		}
		if password == "" {
			return fail(2, "no password on standard input")
		}
	}
	if err := bundle.EnsureRunning(p, bundle.StartOptions{}); err != nil {
		return fail(1, "%v", err)
	}
	result, err := bundle.LocalCall(p, "/api/bundle/account", map[string]string{"email": email, "name": a.flags["name"], "password": password})
	if err != nil {
		return fail(1, "%v", err)
	}
	Print(map[string]any{"id": result["id"], "email": result["email"], "created": result["created"]}, pretty)
	return nil
}

// loginCodeTTL is the default lifetime of a code from `login-code`, in seconds.
const loginCodeTTL = 600

// loginCode issues a one-time sign-in code for `login --url URL --code-stdin`.
func loginCode(p *bundle.Project, a *args, pretty bool) error {
	email := strings.ToLower(a.flags["email"])
	if !emailPattern.MatchString(email) {
		return fail(2, "usage: templatecontext login-code --email EMAIL [--ttl SECONDS]")
	}
	ttl, err := a.intFlag("ttl", loginCodeTTL)
	if err != nil {
		return err
	}
	if !bundle.ValidCodeTTL(ttl) {
		return fail(2, "--ttl must be %d-%d seconds", bundle.MinCodeTTL, bundle.MaxCodeTTL)
	}
	if err := bundle.EnsureRunning(p, bundle.StartOptions{}); err != nil {
		return fail(1, "%v", err)
	}
	result, err := bundle.LocalCall(p, "/api/bundle/login-code", map[string]any{"email": email, "ttl": ttl})
	if err != nil {
		return fail(1, "%v", err)
	}
	Print(map[string]any{"email": result["email"], "code": result["code"], "expires": result["expires"]}, pretty)
	return nil
}

func initProject(p *bundle.Project, a *args, pretty bool) error {
	_, statErr := os.Stat(p.ConfigPath)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if email := a.flags["email"]; email != "" {
		if !emailPattern.MatchString(email) {
			return fail(2, "--email must look like name@example.com (any local address works, e.g. you@example.localhost)")
		}
		p.Config.Email = strings.ToLower(email)
	}
	if a.has("port") {
		port, err := a.intFlag("port", 0)
		if err != nil || port < 1024 || port > 65535 {
			return fail(2, "--port must be 1024-65535")
		}
		p.Config.Port = port
	}
	if p.Config.Email == "" {
		return fail(2, "init needs --email for the local account (any address, e.g. you@example.localhost)")
	}
	if err := os.MkdirAll(p.Dir, 0755); err != nil {
		return err
	}
	if err := p.EnsureInstance(); err != nil {
		return err
	}
	if err := p.SaveConfig(); err != nil {
		return err
	}
	if err := bundle.EnsureRunning(p, bundle.StartOptions{}); err != nil {
		return fail(1, "%v", err)
	}
	// An existing account (for example created on another computer) keeps its password.
	_, existsErr := bundle.LocalCall(p, "/api/bundle/token", map[string]any{})
	created := false
	if existsErr != nil || a.bools["password-stdin"] {
		password, err := readPassword(a.bools["password-stdin"], true)
		if err != nil {
			return err
		}
		result, err := bundle.LocalCall(p, "/api/bundle/account", map[string]string{"email": p.Config.Email, "name": a.flags["name"], "password": password})
		if err != nil {
			return fail(1, "%v", err)
		}
		created, _ = result["created"].(bool)
	}
	if _, err := bundle.LocalCall(p, "/api/bundle/flush", map[string]any{}); err != nil {
		return fail(1, "saving %s: %v", p.DB, err)
	}
	if _, err := New(p); err != nil {
		return err
	}
	Print(map[string]any{"db": p.DB, "config": p.ConfigPath, "storage": p.StorageDir(), "url": p.URL(), "email": p.Config.Email,
		"accountCreated": created, "newProject": fresh, "next": "Run `templatecontext open` to sign in to the reader, and `templatecontext skill` to install the agent skills."}, pretty)
	return nil
}
