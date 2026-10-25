# Origin

Internal maintenance note on where this template comes from.

TemplateContext is a scaffold for new local "*Context" applications. It was derived from PocketContext (github.com/pocketcontext-labs/pocketcontext) at commit `b938b5ebcfa80173b4665491ce232c93a4bd1a68` ("Fix fresh project spaces and reader navigation", 2026-10-22, after release 0.9.1), by copying and trimming:

- Kept, renamed to TemplateContext: the project, working copy, write-back, lease, conflict and recover logic (`internal/bundle`); local authentication (local key, single-use open links, login codes, Host check, own-origin CORS); remote mode (`--url`); the tailcat tunnel (`internal/tunnel`, `tunnel serve|forward`); skill installation; the UI development proxy; the release script, installers, Zig wrapper and license notices; the account hooks (`pb_hooks/account_access*`, `pb_hooks/user_directory*`) and the `users` migration.
- Dropped: spaces and space management, project policy, sources, renditions, passages and ingestion, pages, revisions, publications, citations, page links, properties and catalogs, the audit log, the knowledge commands (`ingest`, `passages`, `stage`, `check-run`, `page`, `publish`, `stage-home`, `lint`, `check`, `get`, `create`, `update`, `batch`, `newid`, `spaces`, `create-personal-space`), Google sign-in and the knowledge reader.
- Added: the `greetings` hello-world collection with its hooks and private full-text index, the `add`, `list` and `search` commands, a test of the SQLite compile options, `scripts/dev.mjs --tunnel`, and, for deriving products, `prompts/new-product.md`, the `template.json` marker and `tests/identity_check.py`.

The vendored server in `internal/pocketcontext/` keeps its upstream name; it comes from github.com/pocketcontext/pocketcontext at commit `20843b7c888daaba55f0646dd77455b7b309d4cc` (see `internal/pocketcontext/VENDORED.md`).

A product derived from this template is a full fork. Review security fixes in PocketContext and in the vendored server and port the relevant ones by hand.
