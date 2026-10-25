# Data model

TemplateContext stores one hello-world collection, `greetings`, whose records are private to the account that created them. The schema lives in `pb_migrations/`; the rules below are enforced by PocketBase collection rules, by `pb_hooks/` and by the SQL and search configuration in `server.json`. A derived product replaces `greetings` with its own collections and keeps the same layers of enforcement.

## Accounts

- `users` is PocketBase's built-in auth collection, configured by `pb_migrations/1800000000_users.js`: password sign-in by email, a required `name` (at most 200 characters), a `disabled` flag and 7-day tokens. All list, create, update, delete and manage rules are closed; an account may view only its own record. Accounts are created by `init` and `account` through the local-key routes in `internal/bundle/local.go`, never through the public API (the PocketBase installer is disabled).
- `pb_hooks/account_access.js` turns an invalid bearer token into an explicit 401 instead of an anonymous request, refuses disabled accounts and revoked sessions on every route (including the SQL and search endpoints), refreshes the token key when an account is disabled or re-enabled, forbids deleting accounts (disable them instead) and filters realtime messages for disabled or revoked sessions.
- `user_directory` (base collection) is a safe projection of `users`: `id` (the same as the account's) and `name`. `pb_hooks/user_directory.js` keeps it in step in the same transaction as every account create, update and delete. It exists because the SQL reader never exposes auth collections; each account can list and view only its own row.

## Greetings

`pb_migrations/1800000100_greetings.js` creates `greetings`:

| Field | Type | Rules |
| --- | --- | --- |
| `id` | text, 15 characters | PocketBase record ID |
| `message` | text | required, at most 500 characters (the CLI also trims whitespace and requires valid UTF-8) |
| `owner` | relation to `users`, one | required; set by the server on create, immutable |
| `created`, `updated` | autodate | set by PocketBase |

An index on `(owner, created)` serves the newest-first list of one owner.

Collection rules, with `signedIn` = `@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.disabled = false`:

- create: `signedIn`;
- list, view, update, delete: `signedIn && owner = @request.auth.id`.

`pb_hooks/greetings.js` (registered in `greetings.pb.js`):

- On a create request it refuses non-`users` callers and sets `owner` to the requester, whatever the client sent, so nobody can create a record for someone else.
- On every update path (REST, batch, internal) it refuses a change of `owner`.

Writes go through the records API, `POST /api/collections/greetings/records` with `{"message": "..."}`; `templatecontext add` does exactly that. The template CLI and reader have no update or delete commands; the rules allow owners to update and delete their own records through the API.

## SQL reads

`server.json` lists the SQL-readable tables and columns:

| Table | Columns |
| --- | --- |
| `greetings` | `id`, `message`, `owner`, `created`, `updated` |
| `user_directory` | `id`, `name` |

`POST /api/context/query {"sql": "SELECT ..."}` (`templatecontext sql`) runs one read-only `SELECT` and returns `{columns, rows, truncated}`; `GET /api/context/schema` (`templatecontext schema`) lists the readable tables, columns and search indexes. Both require a `users` token. The vendored SQL reader (`internal/pocketcontext/sqlread`) authorizes every statement: only listed columns of listed tables, an allowlist of functions, no writes, `PRAGMA`, `ATTACH`, `sqlite_schema`, `dbstat`, `pragma_*` table-valued functions, FTS shadow tables or auth collections. Results are bounded (`maxRows` 500, `maxBytes` 1 MiB, `timeoutMs` 2000); `truncated` says a limit was hit.

### Snapshot filters

The reader never queries the live database. For each requester it builds a filtered snapshot (`permissionModel: filtered-snapshot`) that contains only the rows the `snapshot.filters` in `server.json` select, with `:requester` bound to the signed-in account's ID:

```json
"filters": {
  "greetings": "greetings.owner = :requester",
  "user_directory": "user_directory.id = :requester"
}
```

So `SELECT count(*) FROM greetings` counts only the requester's greetings, joins cannot reach other accounts' rows, and a mistake in a query cannot widen access. The vendored server requires exactly one filter per listed table and refuses to start otherwise. Snapshots are bounded too (`snapshot.maxRows`, `maxBytes`, `timeoutMs`, `maxConcurrent`) and cached until the data changes.

## Search

`server.json` defines one search index:

```json
"greetings": {"isolation": "private", "collection": "greetings", "columns": ["message"], "snippetColumn": "message"}
```

`POST /api/context/search {"index": "greetings", "query": "...", "limit": N, "offset": N, "expectedGeneration": "..."}` (`templatecontext search`) ranks the requester's greetings with SQLite FTS5 (bm25) and returns hits with `id`, `score` and an `excerpt` of `message`, plus `generation` and `hasMore`. A private index is built in memory from the requester's filtered snapshot, so it holds only their own rows; there is no FTS table in the database and nothing to maintain in migrations or hooks. Queries take 1-16 terms, each matched as a whole word, all required, at most 4096 bytes; `limit` is 1-100 and `offset` at most 10,000. The `generation` fingerprints the indexed rows: a continuation passes it as `expectedGeneration`, and HTTP 409 (CLI exit 4) means the requester's greetings changed, so the client restarts at offset 0. The CLI reads the full records of the hits through SQL.

## Reader

The reader (`ui/`) signs in with a password, a single-use link from `open` or a one-time code from `login-code`, lists the account's greetings newest first through SQL, adds one through the records API and searches through the search endpoint. Realtime events on `greetings` refresh the list. It stores its session in `localStorage` under `templatecontext.reader.auth`.

## Adding a collection

To add a collection (or to replace `greetings` in a derived product):

1. **Migration.** Add `pb_migrations/<timestamp>_<name>.js` with a number above the existing ones. Never edit a migration that has shipped; add a new one. Use an `owner` relation to `users` (or another ownership model) and closed rules by default: `signedIn` for create, `signedIn && owner = @request.auth.id` for reads, updates and deletes, or `null` where clients must not act. Add indexes for the reads you expect.
2. **Hooks.** In `pb_hooks/<name>.js` with a `<name>.pb.js` registration, set server-owned fields on create (`e.record.set("owner", e.auth.id)`) and refuse changes to immutable fields on every update path. Hooks run in-process: no `$os` calls (`internal/bundle/hooks_test.go` enforces it).
3. **SQL exposure.** List the readable columns in `server.json` `tables`, never secrets or auth collections, and add a `snapshot.filters` entry that restricts the table to the requester (`<name>.owner = :requester`, or a subquery on a membership table); the server refuses a listed table without one.
4. **Search.** For full-text search add an index under `search.indexes` with `"isolation": "private"`, the collection, its text `columns` and a `snippetColumn`.
5. **Client.** Add commands in `internal/client` (writes through `records(...)`, reads through `c.Rows`/`c.Query` and `/api/context/search`), with entries in `commandHelp` (`help.go`), the overview (`usage.go`) and the argument spec in `commands.go`; `help_test.go` checks they agree.
6. **Reader, skill and tests.** Show the collection in `ui/src`, document the commands in `skills/templatecontext`, and cover ownership, the SQL policy (another account's rows are invisible, writes are refused) and search in `tests/e2e.py`, in `internal/bundle/sqlite_test.go` if the table needs SQLite features, and in the browser tests.

Then run `make ui build test names identity e2e`.
