# New product from pocketcontext-template

Copy this prompt, fill in the first block, and give it to a coding agent that can run shell commands in the folder that contains a checkout of `pocketcontext-template`.

```text
Make a new product named <Name> (lowercase <name>, default port <port>) from pocketcontext-template, with this data model: <description>.
Go module: github.com/<owner>/<repository>   (default: github.com/pocketcontext-labs/<name>)
```

Below, `<Name>` is the display name (for example `NoteContext`), `<name>` the lowercase id (`notecontext`), `<NAME>` its uppercase form (`NOTECONTEXT`), `<port>` the default loopback port (choose one no other *Context product uses; the template uses 8098, PocketContext 8096), `<module>` the Go module path and `<repository>` its last element.

---

You are turning the template repository `pocketcontext-template` into a new local application, **<Name>**. The template is a complete, working product with a placeholder identity (TemplateContext, `templatecontext`, port 8098) and a hello-world data model (`greetings`). Your job is to rename every identity item, replace the hello-world model with the data model above end to end, and prove with the template's own checks that nothing of the template identity is left.

## 0. Read first

In the template, read `AGENTS.md`, `README.md`, `docs/data-model.md`, `docs/origin.md`, `template.json`, `server.json`, `pb_migrations/`, `pb_hooks/`, `internal/client/` (`commands.go`, `help.go`, `usage.go`, `greetings.go`) and `tests/e2e.py`. `AGENTS.md` holds the rules; they apply to the new product unchanged except for names, and you must keep them in its `AGENTS.md`.

## 1. Copy

Create the product as a sibling folder of the template, without its history or build output:

```sh
cd <parent folder of pocketcontext-template>
TEMPLATE_COMMIT=$(git -C pocketcontext-template rev-parse HEAD)
rsync -a --exclude .git --exclude bin --exclude ui/dist --exclude node_modules --exclude ui/test-results \
  --exclude ui/playwright-report --exclude '*.tsbuildinfo' --exclude '__pycache__' --exclude '-.o' \
  pocketcontext-template/ <name>/
cd <name>
git init -b main
rm template.json prompts/new-product.md && rmdir prompts
```

`template.json` marks the template; deleting it switches `tests/identity_check.py` from its inverted template mode to the real check. Do not commit or push until the user asks.

## 2. Rename the identity

Replace every occurrence below. Preserve case variants: `templatecontext` → `<name>`, `TemplateContext` → `<Name>`, `TEMPLATECONTEXT` → `<NAME>`, `pocketcontext-template` → `<repository>` (and the module path `github.com/pocketcontext-labs/pocketcontext-template` → `<module>`), `8098` → `<port>`. Rename files and folders as well as contents. Run `python3 tests/identity_check.py` at any time: it lists every remaining occurrence with its line.

| Item | Where |
| --- | --- |
| Go module path | `go.mod`; every import in `cmd/`, `internal/` (including the import paths inside `internal/pocketcontext/`, which are the only change allowed there), `embed.go`; `-X <module>/internal/bundle.Version` in `Makefile`; `PACKAGE` and `REPOSITORY` in `scripts/release.py`; release download URLs in `.github/workflows/release.yml` |
| Binary and command folder | `cmd/templatecontext/` → `cmd/<name>/`; `bin/templatecontext`, `bin/windows_amd64/templatecontext.exe` in `Makefile`; `exe=` and `-o` in `.github/workflows/test.yml`, `release.yml`, `windows-session.yml`; `executable()` in `scripts/release.py`; default `--binary` in `scripts/dev.mjs` and every test |
| Project files | `DefaultDatabase` `templatecontext.db` and the config name `templatecontext.json` in `internal/bundle/project.go`; the derived `.lease`, `.conflict-*`, `.replaced-*` names follow the database name (check messages and `tests/e2e.py`) |
| Per-machine cache folder | `filepath.Join(base, "templatecontext", ...)` in `internal/bundle/project.go` (working copy, log, local key, `tunnel.json`; `app/` sits beside it) and `internal/client/remote.go` (`remote-<hash>/session.json`) |
| Environment variables | `TEMPLATECONTEXT_DB` (`project.go`), `TEMPLATECONTEXT_URL` (`commands.go`, `tunnel.go`, help), `TEMPLATECONTEXT_DEV_PORT` (`scripts/dev.mjs`, `ui/vite.config.ts`), `TEMPLATECONTEXT_INSTALL_DIR` (`scripts/install.sh.in`, `scripts/install.ps1.in`, `scripts/release.py`, `release.yml`); `SCRUBBED_PREFIXES` in `tests/e2e.py` and the scrubbed prefix in `tests/ui_dev_browser.mjs` and `tests/reader_ui_browser.mjs` |
| Local key header | `KeyHeader = "X-TemplateContext-Local-Key"` in `internal/bundle/local.go` (and its tests) → `X-<Name>-Local-Key` |
| Instance identity | `"app": "templatecontext"` of `/api/bundle/instance` in `internal/bundle/local.go`, and the checks `i.App != "templatecontext"` in `internal/bundle/control.go` and `internal/client/remote.go` and `instance.app === "templatecontext"` in `scripts/dev.mjs` |
| Router hook ID | `templatecontextLocalHost` in `internal/bundle/local.go` |
| Reader session key | `localStorage` key `templatecontext.reader.auth` in `ui/src/api.ts`, in the sign-in page script `loginHTML` in `internal/bundle/local.go`, and in UI and browser tests |
| HTTP client | `User-Agent: TemplateContext/<version>` in `internal/client/client.go` |
| Tracing service | `"service": "templatecontext"` in `server.json` |
| Default port | `DefaultPort` in `internal/bundle/project.go`, `DEFAULT_PORT` in `scripts/dev.mjs`, the fallback in `ui/vite.config.ts`, examples in `internal/client/help.go` and `usage.go`, the skill, tests and docs |
| Reader | package name `templatecontext-reader` in `ui/package.json` (also update `ui/pnpm-lock.yaml` only through `pnpm install`), `<title>` in `ui/index.html`, the wordmark and mark in `ui/src/Brand.tsx` and `ui/src/assets/brand/{logo,favicon}.svg`, sign-in texts in `ui/src/SignIn.tsx`, the `<title>` of `loginHTML`, and the HMR marker in `tests/ui_dev_browser.mjs` |
| Skill | folder `skills/templatecontext/` → `skills/<name>/`; `//go:embed skills/templatecontext` in `embed.go`; `marker = ".templatecontext"`, the three target folders and `root` in `internal/client/skill.go`; `name:` and text of `SKILL.md`, `SKILL.remote.md` and `references/` |
| Help and messages | every `templatecontext ...` and `TemplateContext` string in `internal/client/*.go` (`help.go`, `usage.go`, `commands.go`, `remote.go`, `tunnel.go`, `client.go`), `internal/bundle/*.go`, `internal/tunnel/tunnel.go`, `scripts/dev.mjs`, and the package comment of `cmd/<name>/main.go` |
| Release assets | archive names `templatecontext_<version>_<platform>` and temporary folder prefixes in `scripts/release.py`; the release body text; `templatecontext-release-publication` concurrency group and install folders in `release.yml` |
| Installers | function names `templatecontext_fail`/`_cleanup`/`_install`, temporary names, the default folder `%LOCALAPPDATA%\Programs\TemplateContext` and messages in `scripts/install.sh.in` and `scripts/install.ps1.in` |
| Workflows | binary names and paths in all three `.github/workflows/*.yml`; the `Desktop/TemplateContext` folder in `windows-session.yml`; step names mentioning greetings |
| Tests | temporary folder prefixes, file names and messages in `tests/e2e.py`, `tests/test_release.py`, `tests/dev_launcher.test.mjs`, `tests/ui_dev_browser.mjs`, `tests/reader_ui_browser.mjs`, the usage line of `tests/name_check.py` |
| Documents | `README.md`, `AGENTS.md`, `docs/data-model.md` (rewrite for the new model); the release note in `README.md` keeps `OWNER/REPOSITORY` placeholders until the product has a repository |

Do not change:

- **The vendored server** `internal/pocketcontext/` (package name, code, tests, `VENDORED.md`, `LICENSE`), except its import paths. It is the PocketContext server from github.com/pocketcontext/pocketcontext under the MIT license; keep its name. The release notices name it as "PocketContext server".
- **Security rules**: loopback-only listening, the Host check, own-origin CORS, the local key staying in the private cache, `users` tokens, single-use open links and login codes, remote mode that never opens a database or reads the local key, the tunnel restrictions (one port, `127.0.0.1` only, identity in the cache, address never logged), the `account_access` and `user_directory` hooks, the `users` migration, the SQL reader's denials and `hooks_test.go`'s ban on `$os`.
- **`build-tags.txt`** and the forbidden SQLite tags; `internal/bundle/sqlite_test.go` (adapt only its table names to the new model).
- **`tests/identity_check.py`** and **`tests/name_check.py`** logic. Do not change the needles or logic of `identity_check.py`; the only permitted edit is an `ALLOWED` entry with a reason a reviewer can verify. If you keep a reference to the template on purpose (for example, scrubbing inherited template variables in `tests/e2e.py`), assemble the string from parts as `name_check.py` does (`'TEMPLATE' + 'CONTEXT_'`).
- The retired product name that `tests/name_check.py` checks for must never appear.

## 3. Replace the data model

Replace `greetings` with the collections described above, following "Adding a collection" in `docs/data-model.md`. Every layer must change together; nothing named after greetings may remain.

1. **Migrations.** Delete `pb_migrations/1800000100_greetings.js` and add the product's collections as new numbered files after `1800000000_users.js` (for example `1800000100_<collection>.js`). Keep `1800000000_users.js` and `user_directory`. Records are private to their owner by default: an `owner` relation to `users`, create for signed-in `users`, list/view/update/delete only `owner = @request.auth.id`, unless the data model explicitly requires sharing (then design membership rules and filters as carefully as PocketContext's spaces).
2. **Hooks.** Replace `pb_hooks/greetings.js` and `greetings.pb.js` with hooks that set the owner and other server-managed fields on create and keep immutable fields immutable on every update path. No `$os` calls.
3. **`server.json`.** List only non-secret readable columns under `tables` (keep `user_directory`), give every table a `snapshot.filters` entry restricting it to the requester, and define private search indexes (`"isolation": "private"`) over the text columns worth searching. Rename the tracing service.
4. **CLI.** Replace `internal/client/greetings.go` and the `add`, `list`, `search` commands with the product's commands (writes through the records API, reads through SQL and `/api/context/search`); update the argument spec in `commands.go`, `commandHelp` in `help.go` and the overview in `usage.go` (`help_test.go` checks they agree). Keep `sql`/`query`, `schema`, `whoami` and every server, remote and tunnel command.
5. **Reader.** Replace `ui/src/Greetings.tsx`, `Search.tsx`, the greeting functions in `api.ts`, their tests and styles with the product's views. Keep sign-in, open links, login codes and sign-out.
6. **Skill.** Rewrite `skills/<name>/SKILL.md`, `SKILL.remote.md` and `references/` for the product: when to use it, `ensure-running`, every data command with a verified example, remote mode via tunnel and login code, and the safety rules (never print the local key or passwords, the tunnel address is a secret, SQL is read-only, never touch the database file). Run every documented command against a scratch project.
7. **Tests.** Adapt `tests/e2e.py` (ownership: another account cannot read or change your records; the SQL policy; search and its generation conflict; skill installation; lease, conflict and recover; remote mode; tunnel; login codes), `internal/bundle/sqlite_test.go` (table and FTS names), the UI unit tests and both browser tests (`tests/reader_ui_browser.mjs`, `tests/ui_dev_browser.mjs`).
8. **Docs.** Rewrite `README.md` and `docs/data-model.md` for the product with the same structure and accuracy; adapt `AGENTS.md` (keep every rule, rename, describe the product in its first paragraph, and replace its `## Template` section with one rule: `tests/identity_check.py` must keep passing).

## 4. Record the origin

Rewrite `docs/origin.md` as the product's internal maintenance note: derived from `pocketcontext-labs/pocketcontext-template` at commit `$TEMPLATE_COMMIT` (full SHA and date), which itself derives from PocketContext and the vendored server at the commits the template's `docs/origin.md` and `internal/pocketcontext/VENDORED.md` name; what was kept, renamed, replaced and added. It is the one file allowed to mention the template identity, so say there that security fixes in the template, PocketContext and the vendored server must be ported by hand.

## 5. Check

Run every check and fix what fails until all pass (`make` may need `nix shell nixpkgs#gnumake`; `make windows` needs Zig, e.g. `nix shell nixpkgs#zig`, and a writable `ZIG_GLOBAL_CACHE_DIR`):

```sh
make ui                     # pnpm install --frozen-lockfile (run plain pnpm install once after renaming the package) and build
make build
make test
make names
make e2e                    # runs names and identity first
pnpm --dir ui typecheck && pnpm --dir ui test
pnpm --dir ui exec playwright install chromium
node tests/reader_ui_browser.mjs --binary bin/<name>
node tests/ui_dev_browser.mjs --binary bin/<name>
make windows ZIG=/path/to/zig
ZIG=/path/to/zig CGO_ENABLED=1 GOOS=windows GOARCH=amd64 ZIG_TARGET=x86_64-windows-gnu CC="$PWD/scripts/zig-cc" go vet -tags "$(cat build-tags.txt)" ./...
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" .github/workflows/*.yml   # needs PyYAML
```

Install the skill into a temporary home and read it: `HOME=$(mktemp -d)` with `.claude`, `.codex` and `.gemini` folders, `bin/<name> skill --db <scratch db>` and `bin/<name> skill --url http://127.0.0.1:<port> --agent claude-code` after a remote login; the installed files must name the absolute binary path, contain no `{{` placeholders and describe only real commands.

`git status --short` must not show `bin/`, `ui/dist/`, `node_modules/`, databases, `-.o` or other build output.

## 6. Final verification: leftover identity

The product is not done until this passes:

```sh
python3 tests/identity_check.py --binary bin/<name>
python3 tests/identity_check.py --binary bin/windows_amd64/<name>.exe
```

It must print `ok - no template identity in N files and ...`. It fails on any `templatecontext`, `TemplateContext`, `TEMPLATECONTEXT`, `pocketcontext-template`, `greeting` (case-insensitive) or the template port 8098 in file names, file contents or the binaries, outside its allowlist (`docs/origin.md` and three third-party strings in the binary). If it reports `template mode`, `template.json` was not deleted or `go.mod` still names the template module. Do not weaken the check to make it pass; rename instead.

Report the files you changed, every check's exact result and anything you could not verify.
