# Data model

TemplateContext stores one hello-world collection, `greetings`. A project is single-user: one hidden account reads and writes every record, and every request must still prove it is that account. The schema lives in `pb_migrations/`; the rules below are enforced by PocketBase collection rules, by `pb_hooks/` and by the SQL and search configuration in `server.json`. A derived product replaces `greetings` with its own collections and keeps the same layers of enforcement.

## The account

- `users` is PocketBase's built-in auth collection, configured by `pb_migrations/1810000000_users.js`: password sign-in by email, 7-day tokens, no OAuth2, OTP or MFA. All list, create, update, delete and manage rules are closed; the account may view only its own record. The SQL reader never exposes it.
- It holds exactly one record, created by `init` through the local-key route `POST /api/bundle/account` in `internal/bundle/local.go` (the PocketBase installer is disabled). Its email is the fixed internal address `account@local.invalid` (`bundle.AccountEmail`, mirrored as `ACCOUNT_EMAIL` in `ui/src/api.ts`); nobody chooses or sees it, and the reader and `login --url` ask only for the password. `password` changes the password through `POST /api/bundle/password`, which also revokes earlier tokens.
- `pb_hooks/account_access.js` refuses a second account on every path (REST, superuser, internal saves), forbids deleting the account, turns an invalid bearer token into an explicit 401 instead of an anonymous request on every route (including the SQL and search endpoints), and delivers realtime messages only to authenticated clients.
- There is no account management, no record ownership and no per-requester read filtering. The single account is still required: anonymous requests and invalid tokens get nothing.

## Greetings

`pb_migrations/1810000100_greetings.js` creates `greetings`:

| Field | Type | Rules |
| --- | --- | --- |
| `id` | text, 15 characters | PocketBase record ID |
| `message` | text | required, at most 500 characters (the CLI also trims whitespace and requires valid UTF-8) |
| `created`, `updated` | autodate | set by PocketBase |

An index on `created` serves the newest-first list.

Every collection rule (list, view, create, update, delete) is `signedIn` = `@request.auth.id != '' && @request.auth.collectionName = 'users'`: the project's account may do everything, nobody else anything. Greetings need no hooks.

Writes go through the records API, `POST /api/collections/greetings/records` with `{"message": "..."}`; `templatecontext add` does exactly that. The template CLI and reader have no update or delete commands; the rules allow the account to update and delete greetings through the API.

## SQL reads

`server.json` lists the SQL-readable tables and columns:

| Table | Columns |
| --- | --- |
| `greetings` | `id`, `message`, `created`, `updated` |

`POST /api/context/query {"sql": "SELECT ..."}` (`templatecontext sql`) runs one read-only `SELECT` and returns `{columns, rows, truncated}`; `GET /api/context/schema` (`templatecontext schema`) lists the readable tables, columns and search indexes with `"permissionModel": "shared"`. Both require a `users` token. The vendored SQL reader (`internal/pocketcontext/sqlread`) reads the live working database through read-only connections and authorizes every statement with an SQLite authorizer: only listed columns of listed tables, an allowlist of functions, no writes, `PRAGMA`, `ATTACH`, `sqlite_schema`, `dbstat`, `pragma_*` table-valued functions, FTS shadow tables or auth collections. Results are bounded (`maxRows` 500, `maxBytes` 1 MiB, `timeoutMs` 2000); `truncated` says a limit was hit. The column allowlist is the only read restriction, so list only what the account may read and never secrets.

## Search

`server.json` defines one search index:

```json
"greetings": {"collection": "greetings", "columns": ["message"], "snippetColumn": "message"}
```

`POST /api/context/search {"index": "greetings", "query": "...", "limit": N, "offset": N, "expectedGeneration": "..."}` (`templatecontext search`) ranks greetings with SQLite FTS5 (bm25) and returns hits with `id`, `score` and an `excerpt` of `message`, plus `generation` and `hasMore`. The index is a built index (no `table`): each request reads the indexed columns with fixed SQL in one read transaction of the live database, then searches an in-memory FTS5 index cached by a fingerprint of exactly those rows (`maxIndexBytes` and `cacheBytes` bound it). There is no FTS table in the database and nothing to maintain in migrations or hooks. Every column an index reads must be listed under `tables`. Queries take 1-16 terms, each matched as a whole word, all required, at most 4096 bytes; `limit` is 1-100 and `offset` at most 10,000. The `generation` is that fingerprint: a continuation passes it as `expectedGeneration`, and HTTP 409 (CLI exit 4) means the greetings changed, so the client restarts at offset 0. The CLI reads the full records of the hits through SQL.

A built index may also restrict its rows with `"where"`, a predicate over the collection's SQL-readable columns such as `"archived = 0"` (no parameters, comments or quoted identifiers), and may be scoped by a `scope` record whose JSON object values list member IDs, with `"members": "all"` (every member must exist) or `"visible"` (members filtered out by `where` are skipped). The template uses neither.

## Reader

The reader (`ui/`) signs in with the password, a single-use link from `open` or a one-time code from `login-code`, lists the greetings newest first through SQL, adds one through the records API and searches through the search endpoint. Realtime events on `greetings` refresh the list. It stores its session in `localStorage` under `templatecontext.reader.auth`.

## Databases from other releases

`internal/bundle/schema.go` reads the working copy before PocketBase opens it and refuses the database when its `_migrations` table records a `pb_migrations/*.js` file this binary does not embed: a database of a template release before the single-user schema (whose migrations were `1800000000_users.js` and `1800000100_greetings.js`), of a newer release or of another product. The file is left unchanged and the error says so. Never edit, rename or delete a migration that has shipped; add a new one.

## Adding a collection

To add a collection (or to replace `greetings` in a derived product):

1. **Migration.** Add `pb_migrations/<timestamp>_<name>.js` with a number above the existing ones. Never edit a migration that has shipped; add a new one. Give every rule `signedIn` (`@request.auth.id != '' && @request.auth.collectionName = 'users'`), or `null` where clients must not act; never an empty rule, which would admit anonymous requests. Add indexes for the reads you expect.
2. **Hooks.** Only when the model needs them: in `pb_hooks/<name>.js` with a `<name>.pb.js` registration, set server-managed fields on create and refuse changes to immutable fields on every update path (`onRecordUpdate`, which covers REST, batch and internal saves). Hooks run in-process: no `$os` calls (`internal/bundle/hooks_test.go` enforces it). The runtime is PocketBase's goja, not a browser or Node: there is no `URL` constructor and no `fetch`, so validate addresses with a regular expression. A `json` field's `e.record.get(name)` is the raw JSON bytes, not a parsed value (`Array.isArray` is true and `length` counts bytes): read it with `JSON.parse(String(e.record.get(name)))`, treating an empty string or `null` as unset.
3. **SQL exposure.** List the readable columns in `server.json` `tables`, never secrets or auth collections. The list is the whole read policy: there are no per-requester filters.
4. **Search.** For full-text search add an index under `search.indexes` with the collection, its text `columns` and a `snippetColumn` (and `where` to leave rows out); every column must also be listed under `tables`. Excerpts come only from the `snippetColumn`, so a match in another column is ranked but its words may not appear in the excerpt.
5. **Client.** Add commands in `internal/client` (writes through `records(...)`, reads through `c.Rows`/`c.Query` and `/api/context/search`), with entries in `commandHelp` (`help.go`), the overview (`usage.go`) and the argument spec in `commands.go`; `help_test.go` checks they agree and pins each data command's output keys, so update its expected strings too. `--url` and `--db` are global flags accepted anywhere on the line (`commands.go`), so a record field with one of those names (a bookmark's url) must be positional or use another flag name.
6. **Reader, skill and tests.** Show the collection in `ui/src`, document the commands in `skills/templatecontext`, and cover the rules (anonymous and invalid-token requests are refused, the account reads and writes), the SQL policy (unlisted columns and writes are refused) and search in `tests/e2e.py`, in `internal/bundle/sqlite_test.go` if the table needs SQLite features, and in the browser tests.

Then run `make ui build test names identity e2e`.
