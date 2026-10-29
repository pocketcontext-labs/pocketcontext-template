# New product from pocketcontext-template @VERSION@

Fill in the first block and give this prompt to a coding agent that can run shell commands, in the folder where the product's folder should be created. The agent downloads the template's source itself; no checkout is needed.

This prompt is published with every template release, with its version, commit and date filled in: https://github.com/pocketcontext-labs/pocketcontext-template/releases/latest/download/new-product.md (the newest), or `releases/download/vX.Y.Z/new-product.md` for one version. Use a released copy: in the repository the `@`-placeholders are not filled in.

```text
Make a new product named <Name> (lowercase <name>, default port <port>) from pocketcontext-template, with this data model: <description>.
Go module: github.com/<owner>/<repository>   (default: github.com/pocketcontext-labs/<name>)
```

Below, `<Name>` is the display name (for example `NoteContext`), `<name>` the lowercase id (`notecontext`), `<NAME>` its uppercase form (`NOTECONTEXT`), `<port>` the default loopback port (choose one no other *Context product uses; the template uses 8098, PocketContext 8096), `<module>` the Go module path and `<repository>` its last element.

---

You are turning the template repository `pocketcontext-template` into a new local application, **<Name>**. The template is a complete, working product with a placeholder identity (TemplateContext, `templatecontext`, port 8098) and a hello-world data model (`greetings`). Your job is to rename every identity item, replace the hello-world model with the data model above end to end, and prove with the template's own checks that nothing of the template identity is left.

## 0. Copy

This prompt belongs to template release @VERSION@: commit `@COMMIT@` of @DATE@. Download that release's source archive, check it against the release's `checksums.txt` and unpack it as the product's folder. `<name>` must not exist yet.

```sh
base=https://github.com/pocketcontext-labs/pocketcontext-template/releases/download/v@VERSION@
curl -fsSLO "$base/pocketcontext-template-@VERSION@.tar.gz"
curl -fsSLO "$base/checksums.txt"
grep ' pocketcontext-template-@VERSION@.tar.gz$' checksums.txt | sha256sum -c -   # on macOS: shasum -a 256 -c -
tar -xzf pocketcontext-template-@VERSION@.tar.gz
mv pocketcontext-template-@VERSION@ <name>
rm pocketcontext-template-@VERSION@.tar.gz checksums.txt
cd <name>
git init -b main
```

Stop if the checksum does not match. Do not commit or push until the user asks.

## 1. Read first

In the new folder, read `AGENTS.md`, `README.md`, `docs/data-model.md`, `docs/origin.md`, `template.json`, `server.json`, `pb_migrations/`, `pb_hooks/`, `internal/client/` (`commands.go`, `help.go`, `usage.go`, `greetings.go`) and `tests/e2e.py`. `AGENTS.md` holds the rules; they apply to the new product unchanged except for names, and you must keep them in its `AGENTS.md`.

Then remove the template marker and this prompt:

```sh
rm template.json prompts/new-product.md && rmdir prompts
```

`template.json` marks the template; deleting it switches `tests/identity_check.py` from its inverted template mode to the real check.

## 2. Rename the identity

First delete or rewrite the template-only text, before any bulk replace: a blind replace turns sentences that list the template names into nonsense. That is README "Making a product from the template" and the Layout rows for `prompts/` and `template.json`, the `## Template` section of `AGENTS.md`, and the rows "Template wording" below.

Then replace every occurrence below. Preserve case variants: `templatecontext` → `<name>`, `TemplateContext` → `<Name>`, `TEMPLATECONTEXT` → `<NAME>`, `pocketcontext-template` → `<repository>` (and the module path `github.com/pocketcontext-labs/pocketcontext-template` → `<module>`), `8098` → `<port>` as a whole number only (`identity_check.py` matches it as a token; leave numbers that merely contain those digits). The README's development example uses the port plus 10000 (`18098`); change it to `<port>` + 10000. Rename files and folders as well as contents. Run `python3 tests/identity_check.py` at any time: it lists every remaining occurrence with its line.

| Item | Where |
| --- | --- |
| Go module path | `go.mod`; every import in `cmd/`, `internal/` (including the import paths inside `internal/pocketcontext/`, which are the only change allowed there), `embed.go`; `-X <module>/internal/bundle.Version` in `Makefile`; `PACKAGE` and `REPOSITORY` in `scripts/release.py`; release download URLs in `.github/workflows/release.yml` |
| Binary and command folder | `cmd/templatecontext/` → `cmd/<name>/`; `bin/templatecontext`, `bin/windows_amd64/templatecontext.exe` in `Makefile`; `exe=` and `-o` in `.github/workflows/test.yml`, `release.yml`, `windows-session.yml`; `executable()` in `scripts/release.py`; default `--binary` in `scripts/dev.mjs` and every test |
| Project files | `DefaultDatabase` `templatecontext.db` and the config name `templatecontext.json` in `internal/bundle/project.go`; the derived `.lease`, `.conflict-*`, `.replaced-*` names follow the database name (check messages and `tests/e2e.py`) |
| Per-machine cache folder | `filepath.Join(base, "templatecontext", ...)` in `internal/bundle/project.go` (working copy, log, local key, `tunnel.json`; `app/` sits beside it) and `internal/client/remote.go` (`remote-<hash>/session.json`) |
| Environment variables | `TEMPLATECONTEXT_DB` (`project.go`), `TEMPLATECONTEXT_URL` (`commands.go`, `tunnel.go`, help), `TEMPLATECONTEXT_DEV_PORT` (`scripts/dev.mjs`, `ui/vite.config.ts`), `TEMPLATECONTEXT_INSTALL_DIR` (`scripts/install.sh.in`, `scripts/install.ps1.in`, `scripts/release.py`, `release.yml`); `SCRUBBED_PREFIXES` in `tests/e2e.py` and `scrubbed` in `tests/ui_dev_browser.mjs` and `tests/reader_ui_browser.mjs`: put the new prefix first and keep scrubbing the template prefix, assembled from parts (`'TEMPLATE' + 'CONTEXT_'`) like the PocketContext one |
| Local key header | `KeyHeader = "X-TemplateContext-Local-Key"` in `internal/bundle/local.go` (and its tests, including `tests/e2e.py`) → `X-<Name>-Local-Key` |
| Instance identity | `"app": "templatecontext"` of `/api/bundle/instance` in `internal/bundle/local.go`, and the checks `i.App != "templatecontext"` in `internal/bundle/control.go` and `internal/client/remote.go` and `instance.app === "templatecontext"` in `scripts/dev.mjs` |
| Router hook ID | `templatecontextLocalHost` in `internal/bundle/local.go` |
| Reader session key | `localStorage` key `templatecontext.reader.auth` in `ui/src/api.ts`, in the sign-in page script `loginHTML` in `internal/bundle/local.go`, and in UI and browser tests |
| HTTP client | `User-Agent: TemplateContext/<version>` in `internal/client/client.go` |
| Tracing service | `"service": "templatecontext"` in `server.json` |
| Default port | `DefaultPort` in `internal/bundle/project.go`, `DEFAULT_PORT` in `scripts/dev.mjs`, the fallback in `ui/vite.config.ts`, examples in `internal/client/help.go` and `usage.go`, the skill, tests and docs |
| Reader | package name `templatecontext-reader` in `ui/package.json` (also update `ui/pnpm-lock.yaml` only through `pnpm install`), `<title>` in `ui/index.html`, the wordmark and mark in `ui/src/Brand.tsx` and `ui/src/assets/brand/{logo,favicon}.svg`, sign-in texts in `ui/src/SignIn.tsx`, the `<title>` of `loginHTML`, and the HMR marker in `tests/ui_dev_browser.mjs` |
| Skill | folder `skills/templatecontext/` → `skills/<name>/`; `//go:embed skills/templatecontext` in `embed.go`; `marker = ".templatecontext"`, the three target folders and `root` in `internal/client/skill.go`; `name:` and text of `SKILL.md`, `SKILL.remote.md` and `references/` |
| Help and messages | every `templatecontext ...` and `TemplateContext` string in `internal/client/*.go` (`help.go`, `help_test.go`, `usage.go`, `commands.go`, `remote.go`, `tunnel.go`, `client.go`), `internal/bundle/*.go`, `internal/tunnel/tunnel.go`, `scripts/dev.mjs`, `pb_hooks/account_access.js`, the comment of `pb_migrations/1810000000_users.js`, and the package comment of `cmd/<name>/main.go` |
| Release assets | archive names `templatecontext_<version>_<platform>` and temporary folder prefixes in `scripts/release.py`; the release body text; `templatecontext-release-publication` concurrency group and install folders in `release.yml` |
| Installers | function names `templatecontext_fail`/`_cleanup`/`_install`, temporary names, the default folder `%LOCALAPPDATA%\Programs\TemplateContext` and messages in `scripts/install.sh.in` and `scripts/install.ps1.in` |
| Workflows | binary names and paths in all three `.github/workflows/*.yml`; the `Desktop/TemplateContext` folder in `windows-session.yml`; step names mentioning greetings |
| Tests | temporary folder prefixes, file names and messages in `tests/e2e.py`, `tests/test_release.py`, `tests/dev_launcher.test.mjs`, `tests/ui_dev_browser.mjs`, `tests/reader_ui_browser.mjs`, the usage line of `tests/name_check.py` |
| Template wording | text with no identity needle that only fits the template, which the identity check cannot find: "hello-world template" in `internal/client/usage.go`, "placeholder product" and "(inverted in the template)" in `README.md`, the identity comments in `Makefile` and `.github/workflows/test.yml`, "not part of the template" in `tests/e2e.py`, the "Rename both when deriving a product" comment in `ui/src/Brand.tsx`, the comment of the template's migration, and the speech-bubble drawing of the reader mark |
| Documents | `README.md`, `AGENTS.md`, `docs/data-model.md` (rewrite for the new model); the release note in `README.md` keeps `OWNER/REPOSITORY` placeholders until the product has a repository |

Do not change:

- **The vendored server** `internal/pocketcontext/` (package name, code, tests, `VENDORED.md`, `LICENSE`), except its import paths. It is the PocketContext server from github.com/pocketcontext/pocketcontext under the MIT license; keep its name. The release notices name it as "PocketContext server".
- **Security rules**: loopback-only listening, the Host check, own-origin CORS, the local key staying in the private cache, `users` tokens, single-use open links and login codes, remote mode that never opens a database or reads the local key, the tunnel restrictions (one port, `127.0.0.1` only, identity in the cache, address never logged), the single-user model (exactly one hidden account, refused second accounts, authentication required on every API and collection rule), the `account_access` hooks, the `users` migration, the refusal of databases from other releases (`internal/bundle/schema.go`), the SQL reader's denials and `hooks_test.go`'s ban on `$os`.
- **`build-tags.txt`** and the forbidden SQLite tags. In `internal/bundle/sqlite_test.go` adapt the tables, FTS tables, columns and fixture rows to the new model and the new `server.json`, and keep its denial checks and tag checks as they are.
- **`tests/identity_check.py`** and **`tests/name_check.py`** logic. Do not change the needles or logic of `identity_check.py`; the only permitted edit is an `ALLOWED` entry with a reason a reviewer can verify. If you keep a reference to the template on purpose (for example, scrubbing inherited template variables in `tests/e2e.py`), assemble the string from parts as `name_check.py` does (`'TEMPLATE' + 'CONTEXT_'`).
- The retired product name that `tests/name_check.py` checks for must never appear.

## 3. Replace the data model

Replace `greetings` with the collections described above, following "Adding a collection" in `docs/data-model.md`, which also describes the hook runtime and the global flags. Every layer must change together; nothing named after greetings may remain. Where the description leaves validation, limits or command syntax open, choose, apply the same rule in the hooks, CLI, reader and tests, record it in `docs/data-model.md` and list it in your report.

1. **Migrations.** Delete `pb_migrations/1810000100_greetings.js` and add the product's collections as new numbered files after `1810000000_users.js` (for example `1810000100_<collection>.js`). Keep `1810000000_users.js`. The product is single-user like the template: its one hidden account owns all data, so collections need no `owner` field; give every rule `signedIn` (`@request.auth.id != '' && @request.auth.collectionName = 'users'`), or `null` where clients must not act, and never an empty rule. Do not add accounts, ownership or per-account filtering unless the data model explicitly requires several people (then design accounts, membership rules and read filters as carefully as PocketContext's earlier multi-account spaces).
2. **Hooks.** Only where the model needs them, add `pb_hooks/<collection>.js` with a `<collection>.pb.js` registration that sets server-managed fields on create and keeps immutable fields immutable on every update path. No `$os` calls, and no browser globals such as `URL` (the runtime is goja); decode `json` fields as `docs/data-model.md` describes. Keep `account_access.js` and `account_access.pb.js`, adapting only the product name in its messages.
3. **`server.json`.** List only non-secret readable columns under `tables`: this allowlist is the whole SQL read policy. Define search indexes (built indexes: `collection`, `columns`, `snippetColumn`, optionally `where`) over the text columns worth searching; every column they read must be listed under `tables`. Do not add `snapshot` or `isolation`, which the server rejects. Rename the tracing service.
4. **CLI.** Replace `internal/client/greetings.go` and the `add`, `list`, `search` commands with the product's commands (writes through the records API, reads through SQL and `/api/context/search`); update the argument spec in `commands.go`, `commandHelp` in `help.go` and the overview in `usage.go`, and the expected output keys in `help_test.go`, which checks they agree. `--url` and `--db` are global flags, so a record field with one of those names must be positional or use another flag. Keep `sql`/`query`, `schema`, `whoami` and every server, remote and tunnel command.
5. **Reader.** Replace `ui/src/Greetings.tsx`, `Search.tsx`, the greeting functions in `api.ts`, their tests and styles with the product's views. Keep sign-in, open links, login codes and sign-out.
6. **Skill.** Rewrite `skills/<name>/SKILL.md`, `SKILL.remote.md` and `references/` for the product: when to use it, `ensure-running`, every data command with a verified example, remote mode via tunnel and login code, and the safety rules (never print the local key or passwords, the tunnel address is a secret, SQL is read-only, never touch the database file). After `make build` (section 5), run every documented command against a scratch project.
7. **Tests.** Adapt `tests/e2e.py` (the single account: a second one is refused, anonymous and invalid-token requests to SQL, search and the records API are refused, the account reads and writes every record; the SQL policy; search and its generation conflict; skill installation; lease, conflict and recover; the refused database of another release; remote mode; tunnel; login codes), `internal/bundle/sqlite_test.go` (table and FTS names), the UI unit tests and both browser tests (`tests/reader_ui_browser.mjs`, `tests/ui_dev_browser.mjs`).
8. **Docs.** Rewrite `README.md` and `docs/data-model.md` for the product with the same structure and accuracy; adapt `AGENTS.md` (keep every rule, rename, describe the product in its first paragraph, and replace its `## Template` section with one rule: `tests/identity_check.py` must keep passing).

## 4. Record the origin

Rewrite `docs/origin.md` as the product's internal maintenance note: derived from `pocketcontext-labs/pocketcontext-template` release @VERSION@ (commit `@COMMIT@` of @DATE@), which itself derives from PocketContext and the vendored server at the commits the template's `docs/origin.md` and `internal/pocketcontext/VENDORED.md` name; what was kept, renamed, replaced and added. It is the one file allowed to mention the template identity, so say there that security fixes in the template, PocketContext and the vendored server must be ported by hand.

## 5. Check

Run every check and fix what fails until all pass (`make` may need `nix shell nixpkgs#gnumake`; `make windows` needs Zig, e.g. `nix shell nixpkgs#zig`, and a writable `ZIG_GLOBAL_CACHE_DIR`):

```sh
make ui                     # pnpm install --frozen-lockfile and build (the lockfile does not name the package, so renaming it needs no new install)
make build
make test
make names
make e2e                    # runs names and identity first
(cd ui && pnpm typecheck && pnpm test)        # from ui/, so corepack picks the pnpm that ui/package.json pins
(cd ui && pnpm exec playwright install chromium)
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

It must print `ok - no template identity in N files and ...`. It fails on any `templatecontext`, `TemplateContext`, `TEMPLATECONTEXT`, `pocketcontext-template`, `greeting` (case-insensitive) or the template port 8098 in file names, file contents or the binaries, outside its allowlist (`docs/origin.md` and three third-party strings in the binary). If it reports `template mode`, `template.json` was not deleted and `go.mod` still names the template module. The `greeting` needle is a case-insensitive substring, so it also matches comments, strings, test fixtures and local variables. Do not weaken the check to make it pass; rename instead.

Report the files you changed, every check's exact result and anything you could not verify.
