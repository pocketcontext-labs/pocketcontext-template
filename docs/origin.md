# Origin

Internal maintenance note on where this template comes from.

TemplateContext is a scaffold for new local "*Context" applications. It was derived from PocketContext (github.com/pocketcontext-labs/pocketcontext) at commit `b938b5ebcfa80173b4665491ce232c93a4bd1a68` ("Fix fresh project spaces and reader navigation", 2026-10-22, after release 0.9.1), by copying and trimming:

- Kept, renamed to TemplateContext: the project, working copy, write-back, lease, conflict and recover logic (`internal/bundle`); local authentication (local key, single-use open links, login codes, Host check, own-origin CORS); remote mode (`--url`); the tailcat tunnel (`internal/tunnel`, `tunnel serve|forward`); skill installation; the UI development proxy; the release script, installers, Zig wrapper and license notices; the account hooks (`pb_hooks/account_access*`) and the `users` migration.
- Dropped: spaces and space management, project policy, sources, renditions, passages and ingestion, pages, revisions, publications, citations, page links, properties and catalogs, the audit log, the knowledge commands (`ingest`, `passages`, `stage`, `check-run`, `page`, `publish`, `stage-home`, `lint`, `check`, `get`, `create`, `update`, `batch`, `newid`, `spaces`, `create-personal-space`), Google sign-in and the knowledge reader.
- Added: the `greetings` hello-world collection (at first with ownership hooks and a private full-text index), the `add`, `list` and `search` commands, a test of the SQLite compile options, `scripts/dev.mjs --tunnel`, and, for deriving products, `prompts/new-product.md`, the `template.json` marker and `tests/identity_check.py`.

Later, following PocketContext's move to a single user, the template became single-user too: one hidden account created by `init` (a second one is refused server-side), no `account` command or `--email`/`--name` options, no `user_directory` collection or hooks, `greetings` without an owner and with authenticated-only rules, and databases of the earlier multi-account releases (0.1.x) refused on start (`internal/bundle/schema.go`) instead of migrated. The vendored server was updated with PocketContext commit `d092545083b3b8a8cfa2475fec257469e0fdd515` ("Read the live database directly; drop per-requester snapshots"): SQL reads use the direct engine over the live database, search uses the shared engine with built in-memory indexes, and the filtered snapshots, snapshot cache and tracking, private search and requester admission are gone.

The vendored server in `internal/pocketcontext/` keeps its upstream name; it comes from github.com/pocketcontext/pocketcontext at commit `20843b7c888daaba55f0646dd77455b7b309d4cc` (see `internal/pocketcontext/VENDORED.md`).

A product derived from this template is a full fork. Review security fixes in PocketContext and in the vendored server and port the relevant ones by hand.
