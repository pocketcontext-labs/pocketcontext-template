package client

// Usage is the help text.
const Usage = `templatecontext — a local application in one binary (hello-world template)

The project is the database file (default: templatecontext.db beside this binary,
or --db PATH, or TEMPLATECONTEXT_DB), the storage/ folder and templatecontext.json
beside it. They may live in iCloud Drive, OneDrive, Dropbox or Google Drive.

Setup and server:
  init [--port N] [--password-stdin]
                       create the project and its one account (asks for the
                       reader password), start the server
  ensure-running       start the server in the background unless it runs (alias: start)
  serve [--ui-dev URL]  run in the foreground; optionally proxy a loopback Vite UI
  stop                 write back the database and stop the server
  status               show paths, port, lease and whether the server runs
  open [--no-browser]  open the reader signed in (single-use link, valid for
                       2 minutes); --no-browser prints the link, e.g. to open
                       it on a computer that uses --url
  password [--password-stdin]
                       change the reader password
  login-code [--ttl SECONDS]
                       print a one-time sign-in code (single use, kept in
                       server memory only, default 600 seconds, 30-600) for
                       ` + "`login --url URL --code-stdin`" + `
  flush                write the database back to the synced folder now
  recover --keep local|synced
                       resolve unsaved changes when the synced file also changed
  skill [--agent claude-code|codex|antigravity|all] [--dir DIR]
                       install the agent skill with this computer's paths

Greetings (reads use SQL and full-text search; every request needs a sign-in):
  add MESSAGE|-                      add a greeting (1-500 characters)
  list [--limit N]                   list the greetings, newest first
  search TERMS [--limit N] [--offset N --generation G]
                                     rank the greetings by full-text match
  sql|query SQL|-                    run one read-only SELECT
  whoami | schema                    the session's account ID; the readable schema

Tunnel (carry the server's port to another computer; foreground, Ctrl-C stops):
  tunnel serve [--new-address]
                       on the agent computer: expose only the project's port
                       through tailcat and print the private tunnel address
                       (stable across restarts; --new-address replaces it)
  tunnel forward (ADDRESS | --address-stdin) [--url URL]
                       on the person's computer: listen on the same loopback
                       port (default http://127.0.0.1:8098) and carry it to
                       the agent computer; never uses a local database

Remote client (the server runs on another computer, reached through
` + "`tunnel forward`" + ` or an SSH forward of the same port; --url URL or TEMPLATECONTEXT_URL):
  login --url URL --code-stdin
                       sign in with a one-time code from ` + "`login-code`" + ` on the
                       agent computer, read from standard input; stores only
                       a token in the private cache
  login --url URL [--password-stdin]
                       sign in with the reader password instead
  logout --url URL     forget the stored session
  status --url URL     show whether the server answers and the sign-in
  open --url URL [--no-browser]
                       open the reader (sign in with the password, or open
                       a link from ` + "`open --no-browser`" + ` run on the agent
                       computer)
  skill --url URL [--agent A] [--dir DIR]
                       install the agent skill for the remote server
  Greeting commands work with --url. URL must be http://127.0.0.1:PORT or
  http://localhost:PORT. Remote mode never creates a database or starts a
  server; serve, start, ensure-running, stop, flush, recover, init, password
  and login-code are not available with --url.

Run ` + "`templatecontext help COMMAND`" + ` or ` + "`templatecontext COMMAND --help`" + ` for usage, details and an example.
Options for every command: --db PATH or --url URL, --pretty.
--url and --db exclude each other; --db overrides TEMPLATECONTEXT_URL.
Exit codes: 0 ok, 1 HTTP or transport error, 2 usage, 4 HTTP 409.
`
