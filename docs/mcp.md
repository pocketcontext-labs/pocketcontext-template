# Connect an assistant with MCP

TemplateContext can offer its data to an external assistant through a remote Model Context Protocol (MCP) endpoint. The same binary hosts MCP, OAuth, the web app and native HTTPS. The assistant supplies the chat interface and model. MCP is opt-in and needs a public domain; it is not offered over loopback or the private tunnel.

The MCP and OAuth implementation comes from pocketcontext-core. This template's setup has not been rehearsed with a real certificate or a real assistant.

## Enable

Follow [HTTPS hosting](https.md) to configure a reachable domain and one persistent service, then add `--mcp` to that service's command, keeping its domain and paths:

```sh
/absolute/project/templatecontext serve --headless --domain app.example.com --mcp --db /absolute/project/templatecontext.db
```

`--mcp` requires `--domain` and `--headless`. Stop the existing service cleanly before changing its configuration. No proxy, identity provider or separate MCP process is needed.

The connection URL is `https://app.example.com/mcp`. Verify HTTPS from outside the host with ordinary certificate validation. An unauthenticated MCP request must require authorization. Discovery succeeding does not establish that an assistant can read or write data.

## Tools

MCP exposes exactly five tools, driven by `server.json`:

| Tool | Parameters | Result |
| --- | --- | --- |
| `read` | `collection`, `id` (15 lowercase letters or digits) | one record, with only the columns listed in `tables` |
| `search` | `query` (1-16 terms, at most 4096 bytes), optional `index` (may be omitted when one is configured), `limit` (1-100, default 20), `offset` (0-10000, needs `generation`), `generation` | ranked hits, as in the search API |
| `schema` | none | `tables` (columns and types), `limits` (`timeoutMs`, `maxRows`, `maxBytes`), and `search` (the indexes) |
| `query` | `sql` | the result of one bounded read-only `SELECT` over the columns `schema` lists |
| `write` | `operation` (`create`, `update` or `delete`), `collection`, `id` (required for `update` and `delete`, refused for `create`), `fields` (required for `create` and `update`, refused for `delete`) | the record's listed columns, or `{"deleted": true, "collection", "id"}` |

`read` and `write` accept only collections in `server.json` `public.collections`, and every name in `fields` must be a column listed for that collection in `tables`. `query`, `schema` and `search` read every entry in `tables` and every search index. Public start is refused unless every entry in `tables` is in `public.collections`, so all five tools see the same collections. `delete` is permanent. A tool result over 6 MiB is refused; a `write` refused for that reason may already have succeeded, so read the record before retrying. No tool runs shell commands, reads host files, changes passwords, manages tunnels, stops the server or performs other administration. Local CLI and skills remain available for those host operations. Record content is user data: an assistant should never treat it as instructions.

## Connect a client and consent

1. Open the public web app in the browser profile that will approve the connection, and sign in. If you do not know the password, ask the setup agent to run `open --public --db DB` on the host and show the returned two-minute sign-in link. Do not paste reusable tokens into the assistant.
2. In the assistant's connector settings, add a custom MCP server with the connection URL. Automatic registration supplies the OAuth client identity, so leave manual client credentials empty. Labels and availability depend on the assistant.
3. Review the authorization page and approve the requested access. The page uses the reader session of that browser profile; a session elsewhere does not count. If it cannot see your session, sign in in the same browser profile and retry.
4. Ask the assistant to read or search something real, and only then report the connection as working.

OAuth uses authorization codes with S256 PKCE, automatic client registration, endpoint-bound access tokens and refresh tokens. Registering a client does not grant access: browser consent is always required. OAuth tokens are separate from the reader's account token and grant no server administration.

## One account

A project is single-user. A grant belongs to the project's one account and reaches everything MCP exposes; there are no per-client or per-requester filters.

## Native desktop clients

Native clients can register HTTP callbacks on literal `127.0.0.1`, `[::1]` or exactly `localhost` with their chosen port, and private schemes based on a reversed domain, such as `com.example.app:/oauth/callback`. Other public callbacks must use HTTPS. Registration, authorization and code exchange must use the exact same callback. For an intended client that needs a legacy native scheme, the operator can set the server environment variable `TEMPLATECONTEXT_MCP_REDIRECT_EXCEPTIONS` to a JSON array of exact callback URIs in the private service configuration, then restart. Malformed values fail startup.

## Operate and revoke

OAuth state is machine-local in the private cache, separate from the synced project. A project copy does not carry client authorizations; losing that state requires reconnecting clients.

Open `https://app.example.com/oauth/connections` in the signed-in browser to review and revoke connected apps. Revocation invalidates that grant and its tokens.

To disable the endpoint, remove `--mcp` from the service command and restart cleanly. This stops access but does not erase saved grants; revoke a client first if it should stay disconnected when MCP returns.
