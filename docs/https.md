# HTTPS hosting

TemplateContext can serve its web app on a domain with HTTPS, obtaining a Let's Encrypt certificate and renewing it automatically. The same binary owns the database, reader and HTTP and HTTPS listeners; no reverse proxy or certificate utility is needed. This is opt-in: without `--domain`, the server listens on loopback only.

The HTTPS implementation comes from pocketcontext-core. A pre-release build was rehearsed on a real domain with Linux and systemd: certificate issuance, restart and crash recovery with the same certificate, browser sign-in, and OAuth and MCP from one real client (Grok Bot). Certificate renewal and OAuth token refresh have not been exercised.

## One setup prompt

Give your setup agent this request, replacing the domain:

> Set up TemplateContext for access at app.example.com on this host. Use the installed release's HTTPS hosting manual. Confirm the project folder, inspect existing installations before replacing anything, and preserve existing data. Configure DNS and the host firewall using my authorized tools, asking for missing access only when needed. Install one persistent OS service running TemplateContext as an unprivileged account with permission to bind ports 80 and 443. Before starting the service, make sure the account password has at least 12 characters, at least 5 of them different, set by this release. Verify valid HTTPS from outside the host and that anonymous requests get no data, then give me a fresh `open --public` link. Do not report sign-in as complete until I confirm it. Do not install a reverse proxy.

The agent must read the manual of the exact installed release. A prompt cannot grant missing DNS-provider access or make a host behind CGNAT reachable: detect those prerequisites before promising completion.

## Prerequisites

- A lowercase ASCII DNS hostname you control, such as `app.example.com`. Give `--domain` no scheme, path, port, wildcard or IP address.
- DNS pointing to this host's reachable public address. Publish an AAAA record only when IPv6 actually reaches the listener, and remove stale records before validation.
- Inbound TCP ports 80 and 443 routed to this host, with no conflicting service. A home router may need forwarding; CGNAT may require another hosting location.
- Outbound HTTPS for ACME, correct system time and working DNS.
- An account password with at least 12 characters, at least 5 of them different, set with this release. `init` and `password` accept 8 characters, which is enough on loopback but not here.
- One persistent service under an unprivileged OS account (systemd on Linux). Keep its home and cache stable across restarts so the working copy and certificates survive.

Port 80 serves only ACME validation and redirects to HTTPS. Port 443 serves the web app and its authenticated APIs. Supplying `--domain` accepts the Let's Encrypt subscriber agreement; the optional `--acme-email`, which requires `--domain` and must be a plain address with no display name, is its contact address.

## What is exposed

The public origin answers an allowlist of routes, built from `server.json`:

```json
"public": {"collections": ["greetings"]}
```

- The reader: `/`, `/assets/*`, `/bundle/login`, `/bundle/login.js`, `/api/bundle/auth-mode`.
- Sign-in: `POST /api/bundle/redeem`, `POST /api/collections/users/auth-with-password` and `POST /api/collections/users/auth-refresh`. Password and link attempts are rate-limited per address.
- Authenticated reads: `GET /api/context/schema`, `POST /api/context/query` and `POST /api/context/search`.
- `/api/realtime`, with subscriptions limited to listed collections.
- The record routes of listed collections: list and create on `/api/collections/NAME/records`, and read, update and delete on `/api/collections/NAME/records/ID`.
- With `--mcp` only, `/mcp` and its OAuth and discovery paths.

Every other route answers 404, including a collection that is not listed, `/api/files`, `/api/health`, `/_/` and all local administration.

The list must name every entry in `tables`, because SQL and search read every entry in `tables` on the public origin too, and every name must be in `tables`. So the two lists hold the same collections: to keep a collection off the public origin, remove it from `tables` as well, which also removes it from local SQL. Public start is refused if a listed name is missing from `tables`, invalid or repeated, is the auth collection, or does not exist as an ordinary base collection. Loopback use does not read this list, so these errors appear only when public mode starts.

The column lists in `tables` bind SQL, search and the MCP tools. The record routes return every non-hidden field of a record, so mark a field hidden in the collection, not just unlisted in `tables`, if the signed-in account should not receive it there.

### The local CLI while the service runs

`serve --headless --domain` starts with a private local socket and no loopback TCP port. The local data commands, `open` and `tunnel serve` open the loopback port on the running service the first time they need it, so they keep working on this computer. The port listens on loopback only and is not part of the public origin.

There is no `--tunnel` option; run `tunnel serve` as its own process if you want a private tunnel to a running public service. `--url` accepts only loopback URLs and never the domain, so remote clients use the tunnel, the public origin or MCP.

A project is single-user: the one account created by `init` is the only identity on the public origin, and it reads and writes everything the origin exposes. Every request still needs that account's sign-in.

## Start the service

Initialize a project normally. Public mode checks the account password when it starts, before any listener, and refuses unless it was saved with at least 12 characters, at least 5 of them different. The server can only judge a password when it is saved, so an account from a release before 0.4.0 is refused whatever its password until `password` is run. `password` starts a loopback server if none is running, so stop it afterwards. Stop any running server before handing ownership to the service:

```sh
/absolute/project/templatecontext password --db /absolute/project/templatecontext.db  # when the password is short or predates 0.4.0
/absolute/project/templatecontext stop --db /absolute/project/templatecontext.db
/absolute/project/templatecontext serve --headless --domain app.example.com --db /absolute/project/templatecontext.db
```

`--domain` requires `--headless`, and `--headless` is refused without `--domain`. `--domain` excludes `--tcp-only`, `--port` and `--ui-dev`. The ports are fixed at 80 and 443. Add `--acme-email owner@example.com` if desired. Add `--mcp` only for an explicitly requested assistant connection; see [MCP](mcp.md). The domain and ACME contact are service arguments, not synced project settings. The service manager handles boot and restart; the CLI does not install a service.

## Linux systemd service

Run the service as the actual existing project owner. Replace the paths, `User`, `Group` and domain, and do not casually change a real project's ownership. Save this as `/etc/systemd/system/templatecontext.service`:

```ini
[Unit]
Description=TemplateContext HTTPS web app
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=templatecontext
Group=templatecontext
Environment=HOME=/home/templatecontext
ExecStart=/absolute/project/templatecontext serve --headless --domain app.example.com --db /absolute/project/templatecontext.db
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
Restart=on-failure
RestartSec=5
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
```

Then run `systemctl daemon-reload`, `systemctl enable --now templatecontext.service`, `systemctl status templatecontext.service` and `journalctl -u templatecontext.service` under the host's normal administrator workflow. Do not run the binary as root to solve a low-port permission error.

This unit applies to Linux only. On macOS and Windows, configure the native service manager and firewall yourself, and verify the selected platform's listener permissions and restart behavior; this manual gives no tested steps for them.

## Verify, then sign in

1. Run `status --db PATH` as the service's account and confirm the expected project is running. Listener readiness does not prove that a certificate was issued or that the domain is reachable.
2. From an external network, request `https://app.example.com/` with normal certificate verification, and check that HTTP redirects to that exact HTTPS origin. Never use `curl -k` as evidence of working TLS.
3. Confirm that anonymous requests get no data: `POST /api/context/query` answers 401, a record list of a listed collection answers with no items (HTTP 200 with an empty list is normal), and a route off the allowlist, such as `/api/collections/users/records` or `/_/`, answers 404.
4. Sign in with the project's password, or generate a link on the host:

   ```sh
   /absolute/project/templatecontext open --public --db /absolute/project/templatecontext.db
   ```

   It prints `url` (the public origin), `signInLink`, `expires`, `public` and `note`. It runs on the host only and is refused with `--url`. The `signInLink` expires in two minutes and signs in to the project's one account, so show it only to the user, immediately. The command never starts a server, launches a browser or proves public reachability. Ask the user to open it and confirm that sign-in works.
5. Verify restart, and reboot recovery when practical, through the service manager. A short rehearsal does not prove that a future renewal will succeed.

If a link expires, run `open --public` again. Keep passwords, reusable tokens, certificate keys and the local key out of chat.

## Troubleshooting

- **Port permission denied:** check the service account and its low-port permission. A file capability on a binary can disappear when the binary is replaced.
- **Address already in use:** identify the current owner of the port before changing services. Do not stop an unrelated website to obtain these ports.
- **Certificate not issued:** check DNS, every published address, inbound routing, system time, outbound ACME access and the service logs. Issuance begins when the configured domain is accessed. Do not repeatedly delete the certificate cache, which can trigger issuance limits.
- **`public mode refused`:** the message names the cause. For the password, stop the service, run `password` and then `stop`, and start the service again. For `server.json`, make `public.collections` and `tables` name the same ordinary collections, with no auth or system collection. A refused start exits with an error, so `Restart=on-failure` keeps retrying until the cause is fixed. The password rule is only checked at start: a weaker password set while the service runs takes effect as a refusal at the next restart.
- **Works on the host but not elsewhere:** test from another network. A loopback check is insufficient.

Certificates and ACME state stay in the private machine cache. They are operational secrets, are not part of the synced project and must be re-established on a restored host.
