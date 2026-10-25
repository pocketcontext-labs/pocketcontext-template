# Commands

`templatecontext` below means the command line given in SKILL.md (the absolute binary path with its `--db` or `--url` option). Output is JSON on standard output (`--pretty` indents it); errors go to standard error. Exit codes: 0 ok, 1 HTTP or transport error, 2 usage, 4 conflict (HTTP 409 or a changed search generation). Never paste tokens, codes, passwords or the tunnel address into examples or files.

## Schema

| Table | Columns | Rows you can read |
| --- | --- | --- |
| `greetings` | `id`, `message` (1-500 characters), `owner` (a `users` ID), `created`, `updated` | only your own |
| `user_directory` | `id`, `name` | only your own row |

`templatecontext schema` prints the live tables, columns and search indexes; it takes precedence over this table. Timestamps are UTC text such as `2026-10-25 09:16:27.718Z`. Accounts, credentials and SQLite metadata are not readable.

## add

```sh
templatecontext add "Hello from the command line"
printf '%s\n' "$MESSAGE" | templatecontext add -
```

Adds one greeting owned by the signed-in account (the server sets `owner`; leading and trailing whitespace is trimmed). Prints `{"id", "message", "owner", "created", "updated"}`. An empty or longer than 500-character message exits 2. There are no update or delete commands.

## list

```sh
templatecontext list --limit 10
```

Your greetings, newest first (default 50, at most 500), as an array of `{"id", "message", "owner", "created", "updated"}`.

## search

```sh
templatecontext search "hello" --limit 10
templatecontext search "hello" --limit 10 --offset 10 --generation GENERATION
```

Ranks your greetings by full-text match of `message`: 1-16 terms, each matched as a whole word, all required; `--limit` 1-100 (default 20). Prints `{"query", "generation", "greetings", "hasMore", "nextOffset"}`; each greeting adds `score` and `excerpt`. When `hasMore` is true, request the next page with `--offset` set to `nextOffset` and the same `--generation`. Exit 4 means your greetings changed since the first page: discard the earlier pages and start again at offset 0 without `--generation`.

## sql

```sh
templatecontext sql "SELECT id, message, created FROM greetings ORDER BY created DESC LIMIT 20"
templatecontext sql "SELECT count(*) AS n FROM greetings WHERE message LIKE '%morning%'"
templatecontext sql "SELECT strftime('%Y-%m-%d', created) AS day, count(*) AS n FROM greetings GROUP BY day ORDER BY day"
templatecontext sql "SELECT g.message, u.name FROM greetings g JOIN user_directory u ON u.id = g.owner LIMIT 5"
printf '%s\n' "SELECT id FROM greetings LIMIT 5" | templatecontext sql -
```

One read-only `SELECT` (alias `query`). Prints `{"columns", "rows", "truncated"}`; rows are arrays in column order. Results are bounded (500 rows, 1 MiB): when `truncated` is true a warning is printed; narrow the query or page with `ORDER BY` and `LIMIT`/`OFFSET`. Writes, `PRAGMA`, `sqlite_schema` and unlisted tables or columns are rejected with HTTP 400 (exit 1).

## Account and server

- `whoami`: `{"id", "name", "email", "url"}` of the signed-in account (and `db` locally).
- `status`: locally, paths, port, lease, write-back conflicts and `running`; with `--url`, `reachable` and `loggedIn`.
- `open [--no-browser]`: the reader. Locally it prints a single-use `signInLink` valid for 2 minutes.
- Local only: `ensure-running` (alias `start`), `stop`, `flush`, `recover --keep local|synced` (only on the user's decision), `account`, `login-code`, `password`, `init`, `serve`, `tunnel serve`.
- Remote only: `login --url URL --code-stdin`, `logout --url URL`, `tunnel forward --address-stdin`.
- `skill [--agent claude-code|codex|antigravity|all] [--dir DIR]` reinstalls this skill with the current paths.
