# TemplateContext

A template for local "*Context" applications in one binary, derived from [PocketContext](https://github.com/pocketcontext-labs/pocketcontext). It keeps PocketContext's architecture — a server on PocketBase, a browser reader, a command-line client, a private tunnel to other computers and an agent skill for Claude Code, Codex and Antigravity, all in one Go executable with nothing else needed at run time — and replaces the knowledge base with a hello-world data model: each account adds, lists and searches its own `greetings`.

TemplateContext is a placeholder product. Nobody is meant to use it as it is: a new product copies the repository, renames every identity item and replaces `greetings` with its own collections.

## Making a product from the template

Give an agent [`prompts/new-product.md`](prompts/new-product.md), filled in with the product's name, lowercase id, default port and data model. It copies the template to a sibling folder, renames the identity listed in [`template.json`](template.json) (module path, binary, files, cache folder, environment variables, header, skill, reader, workflows, installers, help and documents), replaces the hello-world collection end to end, records the template commit in `docs/origin.md` and runs every check. The last check, `python3 tests/identity_check.py --binary bin/PRODUCT` (`make identity`), fails while any `templatecontext`, `TemplateContext`, `TEMPLATECONTEXT_`, `pocketcontext-template`, greeting or the template port 8098 remains outside its explicit allowlist. In the template itself (`template.json` present and `go.mod` naming the template module) the check is inverted: it confirms the identity is still there and passes; `--derived` lists everything a product must rename.

A derived product is a full fork. Port security fixes from PocketContext (see [origin](docs/origin.md)) and from the vendored server (`internal/pocketcontext/VENDORED.md`) by hand.

## A project

```
TemplateContext/            ← any folder; may be in iCloud Drive, OneDrive, Dropbox or Google Drive
  templatecontext[.exe]     the binary (optional here; it can live anywhere)
  templatecontext.db        the database
  templatecontext.json      port, account email, project ID (no secrets)
  storage/                  PocketBase file storage (unused by greetings)
```

The database defaults to `templatecontext.db` beside the binary; `--db PATH` or `TEMPLATECONTEXT_DB` selects another. While the server runs, a working copy lives in the per-machine cache (`os.UserCacheDir()/templatecontext/<key>/`) together with the log, the private local key and the tunnel identity; unpacked application files live in `os.UserCacheDir()/templatecontext/app/`. The server writes a consistent copy (`VACUUM INTO` plus an atomic rename) back to `templatecontext.db` a few seconds after changes and on `stop`, so sync clients only ever see complete files.

## Quick start

Build first (see [Build](#build)), then:

```sh
bin/templatecontext init --email you@example.localhost --name "Your Name"   # asks for a reader password
bin/templatecontext open                                                    # reader, signed in
bin/templatecontext add "Hello from the command line"
bin/templatecontext list
bin/templatecontext search "hello"
bin/templatecontext sql "SELECT id, message, created FROM greetings ORDER BY created DESC LIMIT 20"
bin/templatecontext skill                                                   # Claude Code, Codex, Antigravity
bin/templatecontext stop
```

Without `--db`, the database is `bin/templatecontext.db`. The server listens on `http://127.0.0.1:8098` (change `port` in `templatecontext.json`, or `init --port N`). Output is JSON; `--pretty` indents it. Run `templatecontext help` for every command and `templatecontext help COMMAND` for one.

`skill` installs `skills/templatecontext` with this computer's absolute binary path and database into `~/.claude/skills/templatecontext`, `~/.codex/skills/templatecontext` and `~/.gemini/antigravity/skills/templatecontext`, skipping agents whose configuration folder is missing (`--agent` selects one, `--dir` a custom folder). With `--url` it installs the remote variant, `SKILL.remote.md`.

## Authentication

The reader uses the local account's email and password. `open` signs the browser in with a single-use link valid for two minutes; `login-code` issues one-time codes for remote clients (see below). The CLI needs no password: it reads a random key that the server writes to the private cache at start and exchanges it, in the `X-TemplateContext-Local-Key` header, for an ordinary `users` token. Anyone who can read that key can already read the database. All APIs still require a token, CORS allows only the server's own origin, and requests for any other `Host` are refused, so web pages cannot reach the server through `127.0.0.1`. `/api/bundle/instance` answers `{"app": "templatecontext", ...}` so the CLI and the launcher can tell this server from another program on the port.

## Several computers

Only one computer may run a project at a time. `templatecontext.db.lease` carries a heartbeat; another computer refuses to start for two minutes after the last one (`--take-over` overrides when that computer is off). Run `templatecontext stop` before switching computers and wait for the sync client to finish.

If the synced `templatecontext.db` changes while this computer has unsaved changes, nothing is overwritten. This computer's version is kept in `templatecontext.db.conflict-<host>-<time>.db`, the server reports it, and the next start asks for `templatecontext recover --keep local` (the other computer's file is saved as `templatecontext.db.replaced-<time>.db`) or `--keep synced` (this computer's work stays in the conflict copy). Keep the folder available offline: online-only placeholders make the database unavailable.

## Remote client and tunnel

The server can run on one always-on computer (the agent computer) while a person uses the same binary on their own computer as a client only. Sign-in uses one-time codes, so no password passes through whoever operates the agent computer.

On the agent computer, create the person's own account, open the tunnel and issue a code:

```sh
templatecontext account --email you@example.localhost --name "Your Name"   # no password is set or asked for
templatecontext tunnel serve                                               # foreground; prints {"address", "port", ...}
templatecontext login-code --email you@example.localhost                   # prints {"email","code","expires"}
```

On the person's computer, forward the same port and sign in with the code on standard input:

```sh
printf '%s\n' "$ADDRESS" | templatecontext tunnel forward --address-stdin   # foreground; or ssh -L 8098:127.0.0.1:8098 <agent>
printf '%s\n' "$CODE" | templatecontext login --url http://127.0.0.1:8098 --code-stdin
templatecontext list --url http://127.0.0.1:8098
templatecontext skill --url http://127.0.0.1:8098
```

For the browser reader, run `templatecontext open --email you@example.localhost --no-browser` on the agent computer and open the printed `signInLink` on the person's computer, or choose "Use a sign-in code instead" in the reader.

The tunnel is built in: [tailcat](https://github.com/tailscale/tailcat) 0.7.0 (WireGuard over Tailscale's data plane, without a Tailscale account, control plane or administrator rights), bootstrapped through tailcat's public relays, which are best-effort and rate-limited, and direct between the computers when the network allows. `tunnel serve` admits only the project's port and proxies it to the server on `127.0.0.1`; there is no other port, no forwarding through the agent computer and no UDP. Its address embeds the server's public key, a pre-shared key and the relay, so anyone with it can reach the port (the server still requires sign-in): share it privately. The identity is kept in the private cache (`tunnel.json`), never in the project folder, so the address stays the same across restarts of `tunnel serve`, and a running `tunnel forward` reconnects on its own; `--new-address` replaces it and invalidates the old one. `tunnel forward` follows the remote-mode rules: it listens only on the loopback port of `--url` (or `TEMPLATECONTEXT_URL`, default `http://127.0.0.1:8098`), fails when that port is busy instead of choosing another, and never reads a database or local key.

Codes and links are single use and kept only in the server's memory, so a server restart invalidates them. A `login-code` code is valid for 10 minutes (`--ttl 30`-`600` seconds); an `open` link for 2 minutes. `account` without `--password-stdin` never prompts: a new account gets a random password nobody knows, an existing account keeps its password.

A different local port is refused by the server's Host check. `--url` (or `TEMPLATECONTEXT_URL`; an explicit `--db` overrides it) accepts only `http://127.0.0.1:PORT` or `http://localhost:PORT`. Remote mode never resolves or creates a database, never starts a server and never reads the local key. `login` stores only the token, in `os.UserCacheDir()/templatecontext/remote-<hash>/session.json` (private), and asks for a new sign-in when it expires (7 days) or when the URL serves a different project. `logout` deletes it. Server commands (`serve`, `start`, `ensure-running`, `stop`, `flush`, `recover`, `init`, `password`, `account`, `login-code`) run on the agent computer only.

## Data model

`greetings` (`message`, `owner`, `created`, `updated`) are private to their owner: the server sets the owner, collection rules and SQL snapshot filters restrict every read to the requester, and the full-text index is private. `user_directory` mirrors each account's display name. Reads use authenticated read-only SQL (`/api/context/query`) and search (`/api/context/search`); writes use the PocketBase records API. See [docs/data-model.md](docs/data-model.md), including how to add a collection.

## Build

Requires Go (see `go.mod`) with CGO and a C compiler, Node.js 24 and pnpm 10.33.2. GNU Make runs the targets below (`nix shell nixpkgs#gnumake` where it is missing).

```sh
make ui        # pnpm install --frozen-lockfile and build the reader into ui/dist (embedded by go build)
make build     # bin/templatecontext
make test      # Go unit tests (including the vendored server's), launcher tests, release tests
make names     # retired-name check of the repository and bin/templatecontext (tests/name_check.py)
make identity  # template identity check (tests/identity_check.py; inverted in the template)
make e2e       # names and identity, then CLI, remote, tunnel and sync end-to-end tests with synthetic greetings
make windows ZIG=/path/to/zig   # cross-compile bin/windows_amd64/templatecontext.exe
```

`make windows` cross-compiles from Linux or macOS with Zig as the C compiler through `scripts/zig-cc` (target `x86_64-windows-gnu`); get Zig with `nix shell nixpkgs#zig` or from ziglang.org, and set `ZIG_GLOBAL_CACHE_DIR` to a writable scratch folder if the default cache is not writable. Native Windows builds use MinGW-w64 gcc. Check Windows-only code paths with `CGO_ENABLED=1 GOOS=windows GOARCH=amd64 ZIG_TARGET=x86_64-windows-gnu CC="$PWD/scripts/zig-cc" go vet -tags "$(cat build-tags.txt)" ./...` (with `ZIG` set; without CGO the SQLite driver does not compile), and compile the Windows tests with `go test -c` and the same environment.

### Build tags

`build-tags.txt` is the one list of Go build tags every build, test and release uses: SQLite features (first), then the `ts_omit_` tags of the tailcat release in `go.mod`, which leave out tailscale.com features the tunnel does not use (a test checks them against tailcat's own list). PocketContext enables `sqlite_math_functions` and `sqlite_percentile` (math, `median` and `percentile` functions in the SQL reader's allowlist) and `sqlite_fts5` (the private full-text search index). The template adds SQLite features a product may need without changing its build configuration:

| Tag | Enables | Why |
| --- | --- | --- |
| `sqlite_dbstat` | `dbstat` virtual table | measure the space each table and index uses, for maintenance and size diagnostics |
| `sqlite_stat4` | `sqlite_stat4` statistics | better query plans from `ANALYZE` on skewed columns such as `owner` |
| `sqlite_column_metadata` | column origin APIs | map result columns to their table and column in server code |
| `sqlite_introspect` | `pragma_function_list`, `pragma_module_list`, `pragma_pragma_list` | check in tests and diagnostics which functions and modules a build has |
| `sqlite_preupdate_hook` | the pre-update hook | observe row values before and after a change, e.g. for change logs or cache invalidation in Go |
| `sqlite_unlock_notify` | `sqlite3_unlock_notify` | let shared-cache connections wait for a lock instead of failing with `SQLITE_LOCKED` |
| `sqlite_vtable` | Go virtual table modules and R*Tree | implement virtual tables in Go and spatial indexes |

None of them is reachable through the SQL reader: `internal/bundle/sqlite_test.go` checks that every listed tag is compiled in and works, that no unlisted SQLite tag is enabled, and that the reader still denies `dbstat`, `pragma_*` functions, `sqlite_schema`, FTS shadow tables and writes. Never add `icu`, `userauth`, `allow_uri_authority`, `secure_delete`, `vacuum_*`, `foreign_keys`, `app_armor` or `os_trace`: they weaken isolation or change storage semantics, and the test refuses them.

## Develop the reader

Build the executable once with `make ui && make build`, then use Vite hot reload while editing `ui/src`. Choose a separate development project with synthetic data. Initialize it once and stop the server that `init` starts:

```sh
bin/templatecontext init --db /absolute/path/to/dev/templatecontext.db --port 18098 --email dev@example.localhost --name Developer
bin/templatecontext stop --db /absolute/path/to/dev/templatecontext.db
node scripts/dev.mjs --db /absolute/path/to/dev/templatecontext.db
# Equivalent: make dev DB=/absolute/path/to/dev/templatecontext.db
```

The launcher requires an explicit existing database, reads its port from `templatecontext.json`, starts Vite on `127.0.0.1:5173` with `TEMPLATECONTEXT_DEV_PORT` set to that port, then runs the existing binary with `serve --ui-dev http://127.0.0.1:5173`. It refuses occupied ports, waits for readiness and stops its processes on Ctrl-C, using the normal authenticated shutdown to flush database snapshots. It neither installs dependencies nor rebuilds the executable. Use `--binary PATH` for another executable or `--vite-port PORT` when 5173 is occupied. On Windows use `bin/templatecontext.exe` for the initialization commands; the launcher selects that default automatically.

Open the reader at the project port (18098 in this example) and sign in with the development account, or run `bin/templatecontext open --db /absolute/path/to/dev/templatecontext.db` in another terminal. The browser stays on TemplateContext's origin: Go serves authentication and APIs, and proxies UI modules and the hot-reload WebSocket to Vite, which is an internal upstream. `--ui-dev` is a foreground `serve` option, accepts only a loopback HTTP upstream and is never stored in project configuration. Go code, hooks and migrations still need a rebuild and restart.

`node scripts/dev.mjs --db PATH --tunnel [--email EMAIL]` also runs `tunnel serve`, and prints the tunnel address and a one-time sign-in code for that account (default: the email in `templatecontext.json`), so a person on another computer can watch hot reload. They run `tunnel forward --address-stdin --url http://127.0.0.1:PORT` with the **same port** as the project and open `http://127.0.0.1:PORT/bundle/login#CODE`. The same-port rule is not optional: the server accepts only the Host `127.0.0.1:PORT` or `localhost:PORT`, and the development security policy pins the hot-reload WebSocket to `ws://127.0.0.1:PORT`, so a forward on any other port is refused. The address is printed on the terminal only; treat it as a secret.

Hot reload does not type-check. Run `pnpm typecheck`, `pnpm test` and `pnpm build` in `ui/` after changes, plus `node --test tests/dev_launcher.test.mjs` after launcher changes.

The browser checks use synthetic data. Install their pinned Chromium once with `(cd ui && pnpm exec playwright install chromium)`, then run `node tests/reader_ui_browser.mjs --binary bin/templatecontext` (sign-in, adding, listing and searching at desktop and mobile widths; `--screenshots DIR` keeps screenshots) and `node tests/ui_dev_browser.mjs --binary bin/templatecontext` (hot reload and development routing, including a second browser behind a same-port loopback forward and the refusal of a forward on another port). Both accept `--browser /path/to/chromium`; `ui_dev_browser.mjs` also accepts `--playwright /absolute/path/to/playwright/index.mjs`. CI runs both on Linux amd64.

## Tests

| Command | Checks |
| --- | --- |
| `make test` | Go unit tests with the tags in `build-tags.txt` (bundle, client, tunnel, vendored server, SQLite features), `tests/dev_launcher.test.mjs`, `tests/test_release.py` |
| `(cd ui && pnpm typecheck && pnpm test)` | reader types and unit tests (Vitest) |
| `make names` | `tests/name_check.py`: the retired product name appears nowhere in the repository or binary |
| `make identity` | `tests/identity_check.py`: no template identity left in a derived product (inverted in the template) |
| `make e2e` | `tests/e2e.py`: init, CLI, ownership, SQL policy, search, skill, lease, conflict and recover, remote mode, tunnel and login codes, in a temporary folder |
| `node tests/reader_ui_browser.mjs` | the embedded reader in Chromium |
| `node tests/ui_dev_browser.mjs` | hot reload through `serve --ui-dev`, and a same-port loopback forward |
| `make windows` | the Windows cross-compile |

CI (`.github/workflows/test.yml`) runs the reader, unit, name, identity and end-to-end tests on Linux (amd64, arm64), macOS and Windows, and the browser checks on Linux amd64. `.github/workflows/windows-session.yml` opens an interactive Windows session for manual testing over a tailcat tunnel.

## Releasing

`scripts/release.py` and `.github/workflows/release.yml`: pushing a `vX.Y.Z` tag on `main` builds native static binaries on Linux amd64/arm64, macOS arm64 and Windows amd64, runs the name, identity and end-to-end checks against each, assembles `templatecontext_<version>_<platform>.tar.gz`/`.zip`, `install.sh`, `install.ps1` (from `scripts/install.sh.in` and `scripts/install.ps1.in`) and `checksums.txt`, publishes an immutable GitHub release, and then installs it with the one-line installers on every platform and tests again. A manual run of the workflow builds and validates without publishing.

Once a product publishes releases, it installs with:

```sh
curl -fsSL https://github.com/OWNER/REPOSITORY/releases/latest/download/install.sh | sh -s -- "$HOME/TemplateContext"
```

```powershell
$env:TEMPLATECONTEXT_INSTALL_DIR = "$env:OneDrive\TemplateContext"
irm https://github.com/OWNER/REPOSITORY/releases/latest/download/install.ps1 | iex
```

Without a folder, the installers use `~/.local/bin` or `%LOCALAPPDATA%\Programs\TemplateContext`. Each verifies the archive against the release's `checksums.txt` and installs only the executable.

## Layout

| Path | Content |
| --- | --- |
| `cmd/templatecontext` | entry point |
| `internal/bundle` | project resolution, working copy and write-back, lease, local authentication routes, server lifecycle, UI development proxy |
| `internal/client` | CLI commands, greetings, remote mode, skill installation, help |
| `internal/tunnel` | `tunnel serve` and `tunnel forward` on tailcat: identity, port filter, loopback proxy |
| `internal/pocketcontext` | vendored PocketContext server (SQL reads, snapshots, search); see `VENDORED.md` |
| `pb_hooks`, `pb_migrations` | schema and server-side rules |
| `server.json` | SQL allowlist, snapshot filters and search index configuration |
| `ui` | browser reader (React 19, Vite) |
| `embed.go` | embeds `pb_hooks`, `pb_migrations`, `server.json`, `ui/dist` and `skills/templatecontext` |
| `skills/templatecontext` | skill template (`SKILL.remote.md` for `skill --url`); `templatecontext skill` fills in absolute paths |
| `scripts` | `dev.mjs` launcher, `release.py`, installer templates, `zig-cc` wrapper, license notices |
| `tests` | `e2e.py`, `name_check.py`, `identity_check.py`, `test_release.py`, `dev_launcher.test.mjs`, browser checks |
| `prompts/new-product.md` | the prompt that turns this template into a product |
| `template.json` | marks the template and lists its identity; deleted in a derived product |
