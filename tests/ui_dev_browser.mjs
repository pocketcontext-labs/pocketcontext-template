#!/usr/bin/env node
// Real-browser development-mode check. Install Playwright separately or pass
// --playwright /absolute/path/to/playwright/index.mjs. An existing Chromium can
// be selected with --browser /absolute/path/to/chromium. Synthetic data only.
//
// Same-port forward simulation. `tunnel forward` gives the other computer
// 127.0.0.1:P, carried byte for byte to the agent computer's 127.0.0.1:P.
// On one computer both ends cannot listen on 127.0.0.1:P, and unprivileged
// network namespaces are not generally available (CI, AppArmor). So the second
// browser runs as "the other computer" behind a SOCKS5 forward on another
// loopback address (127.0.0.2). Chromium connects to every origin, loopback
// included, only through that forward, which relays raw TCP bytes to the
// server like the tunnel does. The browser itself still navigates to
// http://127.0.0.1:P, so its Host and Origin headers, the security policy it
// enforces and the hot-reload WebSocket URL are exactly those of a person
// behind a real same-port forward. The forward records the requests it carries
// to prove that page loads, API calls and the HMR WebSocket all crossed it.
// A forward that listens on another port Q (the browser opens 127.0.0.1:Q and
// the bytes still reach P) must be refused by the server's Host check.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const args = process.argv.slice(2);
function option(name, fallback) {
  const index = args.indexOf(name);
  if (index < 0) return fallback;
  assert(args[index + 1], `${name} requires a value`);
  return args[index + 1];
}
const binary = path.resolve(option('--binary', path.join(root, 'bin', process.platform === 'win32' ? 'templatecontext.exe' : 'templatecontext')));
const playwrightPath = option('--playwright');
const uiRequire = createRequire(path.join(root, 'ui/package.json'));
const { chromium } = playwrightPath ? await import(pathToFileURL(path.resolve(playwrightPath)).href) : uiRequire('playwright');
const temporary = await mkdtemp(path.join(os.tmpdir(), 'templatecontext-ui-dev-'));
const home = path.join(temporary, 'home');
const ui = path.join(temporary, 'ui');
const db = path.join(temporary, 'project', 'templatecontext.db');
await Promise.all([mkdir(home), mkdir(ui), mkdir(path.dirname(db))]);
// Inherited product variables would redirect the binary; scrub them as tests/e2e.py does.
const scrubbed = ['TEMPLATECONTEXT_', 'POCKET' + 'CONTEXT_'];
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !scrubbed.some(prefix => key.startsWith(prefix))));
Object.assign(env, { HOME: home, USERPROFILE: home, XDG_CACHE_HOME: path.join(home, 'cache'), LOCALAPPDATA: path.join(home, 'AppData', 'Local') });
const children = [];
const browsers = [];
const pages = [];
let forward;

async function freePort(host = '127.0.0.1') {
  const server = net.createServer();
  server.listen(0, host);
  await once(server, 'listening');
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}
function start(command, argv, options = {}) {
  const child = spawn(command, argv, { stdio: ['pipe', 'pipe', 'pipe'], ...options });
  child.output = '';
  child.stdout.on('data', chunk => { child.output += chunk; });
  child.stderr.on('data', () => {}); // No URLs, tokens or server request logs in test output.
  children.push(child);
  return child;
}
async function cli(argv, input) {
  const child = start(binary, ['--db', db, ...argv], { env, cwd: temporary });
  const ended = once(child, 'exit');
  child.stdin.end(input);
  const timeout = setTimeout(() => child.kill('SIGKILL'), 120_000);
  try {
    const [code] = await ended;
    assert.equal(code, 0, `CLI ${argv[0]} failed`);
    return child.output.trim() ? JSON.parse(child.output) : null;
  } finally { clearTimeout(timeout); }
}
async function until(check, description, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check().catch(() => false)) return;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`Timed out: ${description}`);
}
function ok(message) { console.log(`ok - ${message}`); }

/**
 * A SOCKS5 front for "the other computer". `routes` maps the port the browser
 * asks for (the forward's listening port) to the agent-side server port.
 * Unknown destinations are refused, so nothing bypasses the forward silently.
 */
async function startForward(routes) {
  const carried = [];
  const refused = [];
  const sockets = new Set();
  const server = net.createServer(socket => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    socket.on('error', () => {});
    socket.once('data', handshake => {
      if (handshake[0] !== 5) return socket.destroy();
      socket.write(Buffer.from([5, 0])); // No authentication.
      socket.once('data', request => {
        let host, port;
        if (request[1] !== 1) return socket.end(Buffer.from([5, 7, 0, 1, 0, 0, 0, 0, 0, 0]));
        if (request[3] === 1) { host = [...request.subarray(4, 8)].join('.'); port = request.readUInt16BE(8); }
        else if (request[3] === 3) { const n = request[4]; host = request.subarray(5, 5 + n).toString(); port = request.readUInt16BE(5 + n); }
        const target = routes.get(port);
        if (!['127.0.0.1', 'localhost'].includes(host) || !target) {
          refused.push(`${host}:${port}`);
          return socket.end(Buffer.from([5, 2, 0, 1, 0, 0, 0, 0, 0, 0]));
        }
        const upstream = net.connect(target, '127.0.0.1', () => {
          socket.write(Buffer.from([5, 0, 0, 1, 0, 0, 0, 0, 0, 0]));
          // Record request heads only; bodies and tokens are not kept.
          socket.on('data', chunk => {
            const text = chunk.toString('latin1');
            const line = /^(GET|POST|PATCH|PUT|DELETE|HEAD|OPTIONS) (\S+) HTTP\/1\.1\r\n/.exec(text);
            if (!line) return;
            const header = name => new RegExp(`^${name}: (.*)$`, 'im').exec(text)?.[1]?.trim();
            carried.push({ via: port, method: line[1], path: line[2], host: header('Host'), origin: header('Origin'), upgrade: header('Upgrade') });
          });
          socket.pipe(upstream);
          upstream.pipe(socket);
        });
        upstream.on('error', () => socket.destroy());
        socket.on('close', () => upstream.destroy());
      });
    });
  });
  server.listen(0, '127.0.0.2');
  await once(server, 'listening');
  return {
    carried, refused,
    url: `socks5://127.0.0.2:${server.address().port}`,
    close: () => { for (const socket of sockets) socket.destroy(); return new Promise(resolve => server.close(resolve)); },
  };
}
/** A raw request whose Host header is chosen by the caller, as a forward on another port would send. */
function rawRequest(port, host, headers = {}) {
  return new Promise((resolve, reject) => {
    const request = http.request({ host: '127.0.0.1', port, path: '/', headers: { Host: host, ...headers } }, response => {
      response.resume();
      resolve(response.statusCode);
    });
    request.on('upgrade', (response, socket) => { socket.destroy(); resolve(101); });
    request.on('error', reject);
    request.end();
  });
}

try {
  const port = await freePort();
  const vitePort = await freePort();
  const otherPort = await freePort();
  const origin = `http://127.0.0.1:${port}`;
  const upstream = `http://127.0.0.1:${vitePort}`;
  for (const name of ['src', 'index.html', 'package.json', 'vite.config.ts']) {
    await cp(path.join(root, 'ui', name), path.join(ui, name), { recursive: true });
  }
  await symlink(path.join(root, 'ui/node_modules'), path.join(ui, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  await cli(['init', '--port', String(port), '--password-stdin'], 'synthetic browser password\n');
  await cli(['stop']);
  const startVite = () => start(process.execPath, [path.join(root, 'ui/node_modules/vite/bin/vite.js'), '--host', '127.0.0.1', '--port', String(vitePort)], {
    cwd: ui, env: { ...process.env, TEMPLATECONTEXT_DEV_PORT: String(port) },
  });
  let vite = startVite();
  await until(async () => (await fetch(upstream)).ok, 'Vite readiness');
  let server = start(binary, ['serve', '--db', db, '--ui-dev', upstream], { env, cwd: temporary });
  await until(async () => (await fetch(`${origin}/api/bundle/instance`)).ok, 'TemplateContext readiness');
  await cli(['add', 'Hello from the synthetic CLI']);

  const browser = await chromium.launch({ headless: true, executablePath: option('--browser') });
  browsers.push(browser);
  const context = await browser.newContext();
  const page = await context.newPage();
  pages.push(page);
  const sockets = [];
  const requests = [];
  const errors = [];
  page.on('websocket', socket => sockets.push(socket.url()));
  page.on('request', request => requests.push(new URL(request.url())));
  page.on('pageerror', error => errors.push(error.message));
  const signIn = await cli(['open', '--no-browser']);
  await page.goto(signIn.signInLink);
  await page.getByRole('button', { name: 'Sign out' }).waitFor();
  await page.getByText('Hello from the synthetic CLI', { exact: true }).waitFor();
  ok('one-time open link signs into the reader on the backend origin');
  await until(async () => sockets.some(url => new URL(url).pathname === '/__ui_hmr'), 'same-origin hot-reload connection');
  assert(sockets.every(url => new URL(url).port === String(port)), 'HMR must not bypass the backend');
  assert(requests.every(url => url.origin === origin), 'all UI and API requests must use the backend origin');
  ok('Vite modules and hot-reload WebSocket use the backend origin');

  const documentMarker = await page.evaluate(() => (window.__uiTestDocument = crypto.randomUUID()));
  const initialPID = server.pid;
  const css = path.join(ui, 'src/styles.css');
  await writeFile(css, `${await readFile(css, 'utf8')}\nbody { --templatecontext-hmr-check: updated; }\n`);
  await page.waitForFunction(() => getComputedStyle(document.body).getPropertyValue('--templatecontext-hmr-check').trim() === 'updated');
  const component = path.join(ui, 'src/Brand.tsx');
  const source = await readFile(component, 'utf8');
  assert(source.includes('<span>TemplateContext</span>'));
  await writeFile(component, source.replace('<span>TemplateContext</span>', '<span>SYNTHETIC HOT UPDATE</span>'));
  await page.getByText('SYNTHETIC HOT UPDATE', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => window.__uiTestDocument), documentMarker, 'CSS and component edits must preserve the document');
  assert.equal(server.pid, initialPID);
  assert.equal(server.exitCode, null, 'the same Go process must stay running');
  await page.getByRole('button', { name: 'Sign out' }).waitFor();
  assert.equal(errors.length, 0, `reader must have no browser runtime errors: ${errors.join('; ')}`);
  ok('CSS and React edits update without navigation, sign-in loss or Go restart');

  await until(async () => requests.some(url => url.pathname === '/api/realtime'), 'reader realtime connection');
  await cli(['add', 'Realtime greeting from the CLI']);
  await page.getByText('Realtime greeting from the CLI', { exact: true }).waitFor();
  ok('greeting realtime updates still reach the reader');

  // The other computer: a separate browser whose every connection crosses the same-port forward.
  forward = await startForward(new Map([[port, port], [otherPort, port]]));
  const remoteBrowser = await chromium.launch({ headless: true, executablePath: option('--browser'), proxy: { server: forward.url } });
  browsers.push(remoteBrowser);
  const remote = await (await remoteBrowser.newContext()).newPage();
  pages.push(remote);
  const remoteSockets = [];
  const remoteRequests = [];
  const remoteErrors = [];
  remote.on('websocket', socket => remoteSockets.push(socket.url()));
  remote.on('request', request => remoteRequests.push(new URL(request.url())));
  remote.on('pageerror', error => remoteErrors.push(error.message));
  await remote.goto(origin);
  // The remote person signs in with a one-time code issued on the agent computer.
  const code = await cli(['login-code']);
  await remote.getByRole('button', { name: 'Use a sign-in code instead' }).click();
  await remote.getByLabel('Sign-in code').fill(code.code);
  await remote.getByRole('button', { name: 'Sign in' }).click();
  await remote.getByRole('button', { name: 'Sign out' }).waitFor();
  await remote.getByText('Hello from the synthetic CLI', { exact: true }).waitFor();
  ok('through the same-port forward a one-time login code signs in the project account');

  await until(async () => remoteSockets.some(url => new URL(url).pathname === '/__ui_hmr'), 'hot reload over the forward');
  assert(remoteSockets.every(url => new URL(url).origin === `ws://127.0.0.1:${port}`), 'the HMR WebSocket stays same-origin over the forward');
  assert(remoteRequests.every(url => url.origin === origin), 'the forwarded reader uses only its own origin');
  const hmr = forward.carried.find(item => item.path.split('?')[0] === '/__ui_hmr');
  assert(hmr, 'the HMR WebSocket must cross the forward');
  assert.equal(hmr.host, `127.0.0.1:${port}`);
  assert.equal(hmr.origin, origin);
  assert.equal(hmr.upgrade?.toLowerCase(), 'websocket');
  for (const pathname of ['/', '/@vite/client', '/api/bundle/redeem', '/api/context/query', '/api/realtime'])
    assert(forward.carried.some(item => item.path.split('?')[0] === pathname), `${pathname} must cross the forward`);
  assert(forward.carried.every(item => item.host === `127.0.0.1:${port}` && item.via === port), 'every forwarded request keeps Host 127.0.0.1:P');
  ok('page, modules, API, realtime and HMR WebSocket all cross the forward with Host 127.0.0.1:P');

  const remoteMarker = await remote.evaluate(() => (window.__uiTestDocument = crypto.randomUUID()));
  const updated = await readFile(component, 'utf8');
  await writeFile(component, updated.replace('<span>SYNTHETIC HOT UPDATE</span>', '<span>FORWARDED HOT UPDATE</span>'));
  await remote.getByText('FORWARDED HOT UPDATE', { exact: true }).waitFor();
  await writeFile(css, `${await readFile(css, 'utf8')}\nbody { --templatecontext-hmr-check: forwarded; }\n`);
  await remote.waitForFunction(() => getComputedStyle(document.body).getPropertyValue('--templatecontext-hmr-check').trim() === 'forwarded');
  assert.equal(await remote.evaluate(() => window.__uiTestDocument), remoteMarker, 'forwarded hot updates must preserve the document');
  await page.getByText('FORWARDED HOT UPDATE', { exact: true }).waitFor();
  await remote.getByLabel('New greeting').fill('Hello across the forward');
  await remote.getByRole('button', { name: 'Add' }).click();
  await remote.getByText('Hello across the forward', { exact: true }).waitFor();
  await remote.getByRole('searchbox', { name: 'Search greetings' }).fill('forward');
  await remote.getByRole('button', { name: 'Search' }).click();
  await remote.getByRole('list', { name: 'Search results' }).getByText('Hello across the forward').waitFor();
  assert.equal(remoteErrors.length, 0, `forwarded reader must have no runtime errors: ${remoteErrors.join('; ')}`);
  assert.equal(server.pid, initialPID);
  ok('hot reload, add and search work through the same-port forward');

  // Negative: a forward listening on another port still delivers bytes to P, but the Host check refuses it.
  const wrong = `http://127.0.0.1:${otherPort}`;
  const refusedPage = await (await remoteBrowser.newContext()).newPage();
  const response = await refusedPage.goto(wrong);
  assert.equal(response.status(), 403, 'a different-port forward must be refused');
  assert.match(await refusedPage.content(), /Unexpected host/);
  assert(forward.carried.some(item => item.via === otherPort && item.host === `127.0.0.1:${otherPort}`), 'the refused request did reach the server through the forward');
  assert.equal(await rawRequest(port, `127.0.0.1:${otherPort}`, { Connection: 'Upgrade', Upgrade: 'websocket', Origin: wrong, 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'c3ludGhldGljLWtleS0xMjM0' }), 403, 'the HMR WebSocket is refused for another port');
  assert.equal(await rawRequest(port, `localhost:${port}`), 200, 'localhost on the same port is accepted');
  const policy = (await fetch(origin)).headers.get('content-security-policy');
  assert.match(policy, new RegExp(`connect-src 'self' ws://127\\.0\\.0\\.1:${port} ws://localhost:${port}`), 'the development policy pins the WebSocket port');
  ok('a forward on a different port is refused by the Host check; the policy pins the port');
  await forward.close();
  forward = undefined;

  const viteStopped = once(vite, 'exit');
  vite.kill('SIGTERM');
  await viteStopped;
  await until(async () => (await fetch(origin)).status === 502, 'clear unavailable Vite response');
  assert((await fetch(`${origin}/api/bundle/instance`)).ok, 'Go API stays available if Vite stops');
  ok('Vite failure returns 502 while the backend remains healthy');
  vite = startVite();
  await until(async () => (await fetch(`${origin}/@vite/client`)).ok, 'Vite restart');
  await page.getByRole('button', { name: 'Sign out' }).waitFor();
  await writeFile(css, `${await readFile(css, 'utf8')}\nbody { --templatecontext-hmr-check: reconnected; }\n`);
  await page.waitForFunction(() => getComputedStyle(document.body).getPropertyValue('--templatecontext-hmr-check').trim() === 'reconnected');
  assert.equal(server.pid, initialPID);
  assert.equal(server.exitCode, null);
  assert(sockets.every(url => new URL(url).port === String(port)));
  assert.equal(errors.length, 0, 'Vite restart must have no browser runtime errors');
  ok('hot reload recovers after restarting Vite with the same Go process');

  await cli(['stop']);
  await until(async () => server.exitCode !== null, 'development server shutdown');
  server = start(binary, ['serve', '--db', db], { env, cwd: temporary });
  await until(async () => (await fetch(`${origin}/api/bundle/instance`)).ok, 'embedded server readiness');
  const embedded = await fetch(origin);
  assert.equal(embedded.status, 200);
  assert(!(await embedded.text()).includes('/@vite/client'));
  const embeddedPage = await (await browser.newContext()).newPage();
  const embeddedSignIn = await cli(['open', '--no-browser']);
  await embeddedPage.goto(embeddedSignIn.signInLink);
  await embeddedPage.getByRole('button', { name: 'Sign out' }).waitFor();
  await embeddedPage.locator('.product-brand img').evaluate(async image => { await image.decode(); });
  assert(await embeddedPage.locator('.product-brand img').evaluate(image => image.naturalWidth > 0), 'embedded logo must render under the production security policy');
  ok('the same binary still serves the signed-in embedded reader by default');
  console.log('all UI development browser checks passed');
} catch (error) {
  // Visible alerts explain most failures; they never contain codes or tokens.
  for (const page of pages) {
    const alerts = await page.getByRole('alert').allTextContents().catch(() => []);
    if (alerts.length) console.error(`reader alerts at ${page.url().split('#')[0]}: ${alerts.join(' | ')}`);
    if (process.env.UI_TEST_DEBUG) console.error(await page.locator('body').innerText().catch(() => ''));
  }
  throw error;
} finally {
  for (const browser of browsers) await browser.close().catch(() => {});
  await forward?.close();
  await cli(['stop']).catch(() => {});
  for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await Promise.all(children.map(child => child.exitCode === null && child.signalCode === null ? once(child, 'exit').catch(() => {}) : undefined));
  await rm(temporary, { recursive: true, force: true });
}
