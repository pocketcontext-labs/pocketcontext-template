# Vendored PocketContext server

Copied from github.com/pocketcontext/pocketcontext at commit
20843b7c888daaba55f0646dd77455b7b309d4cc, under its MIT license (LICENSE),
by way of github.com/pocketcontext-labs/pocketcontext at commit
b938b5ebcfa80173b4665491ce232c93a4bd1a68, whose copy this is, unchanged except
for import paths, which were rewritten to this module, and then updated to
github.com/pocketcontext-labs/pocketcontext commit
d092545083b3b8a8cfa2475fec257469e0fdd515 (direct SQL reads over the live
database; built in-memory search indexes in the shared engine; no filtered
snapshots, snapshot tracking or private search). The package keeps its
upstream name; do not rename it when deriving a product. For the origin of the
application (hooks, migrations, reader, CLI) see [docs/origin.md](../../docs/origin.md).

Local changes (inherited from pocketcontext-labs/pocketcontext unless noted):

- `sqliteuri/`: SQLite `file:` URIs built with forward slashes. `url.URL{Path: "C:\\..."}`
  encodes a Windows drive path as a URI authority, which SQLite rejects.
- `database/` and `tracing/`: platform files. Windows has no `O_NOFOLLOW`, and
  its mode bits cannot prove that a file is private.
- `server.RegisterDatabase`: reads SQL from an explicit database path, because
  the application keeps its live database in a per-machine cache.
- Test and benchmark names and fixtures contain no application product names.
- `searchread/` (this template only): without PocketContext's source-content projection
  (`sourceContent`: sources, renditions and passages scoped by space, with
  passage hits), which names PocketContext's own tables. Built indexes keep
  `where`, `scope` and `members`.

Review upstream security fixes to the SQL authorizer and search
and port them here by hand. `internal/bundle/sqlite_test.go` checks that the
SQLite features enabled in `build-tags.txt` stay out of reach of the SQL reader.
