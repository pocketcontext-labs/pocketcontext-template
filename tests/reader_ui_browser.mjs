#!/usr/bin/env node
// Synthetic real-browser check of the built (embedded) reader. Run after
// make ui && make build, and pnpm --dir ui exec playwright install chromium.
// No real project is opened. Options: --binary PATH, --browser PATH,
// --screenshots DIR (keep screenshots there).
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
function option(name, fallback) {
  const index = process.argv.indexOf(name);
  if (index < 0) return fallback;
  assert(process.argv[index + 1], `${name} requires a value`);
  return process.argv[index + 1];
}
const binary = path.resolve(option('--binary', path.join(root, 'bin', process.platform === 'win32' ? 'templatecontext.exe' : 'templatecontext')));
const { chromium } = createRequire(path.join(root, 'ui/package.json'))('playwright');
const temporary = await mkdtemp(path.join(os.tmpdir(), 'templatecontext-reader-test-'));
const screenshotOption = option('--screenshots');
const screenshots = screenshotOption ? path.resolve(screenshotOption) : path.join(temporary, 'screenshots');
const home = path.join(temporary, 'home');
const db = path.join(temporary, 'project', 'templatecontext.db');
await Promise.all([mkdir(home), mkdir(path.dirname(db)), mkdir(screenshots, { recursive: true })]);
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('TEMPLATECONTEXT_')));
Object.assign(env, { HOME: home, USERPROFILE: home, XDG_CACHE_HOME: path.join(home, 'cache'), LOCALAPPDATA: path.join(home, 'AppData', 'Local') });
const children = [];
let browser;

async function freePort() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}
function start(command, argv, options = {}) {
  const child = spawn(command, argv, { stdio: ['pipe', 'pipe', 'pipe'], ...options });
  child.output = '';
  child.stdout.on('data', chunk => { child.output += chunk; });
  child.stderr.on('data', () => {}); // Never print authentication URLs or request logs.
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
function ok(message) { console.log(`ok - ${message}`); }
const key = 'templatecontext.reader.auth';

try {
  const port = await freePort();
  const origin = `http://127.0.0.1:${port}`;
  const password = 'synthetic reader password';
  // init starts the server in the background with the embedded reader.
  await cli(['init', '--email', 'reader@example.localhost', '--name', 'Reader Test', '--port', String(port), '--password-stdin'], `${password}\n`);
  await cli(['account', '--email', 'other@example.localhost', '--name', 'Other Reader']);
  await cli(['add', 'A greeting from the CLI']);

  const served = await fetch(origin);
  assert.equal(served.status, 200);
  const html = await served.text();
  assert(!html.includes('/@vite/client'), 'the embedded reader is served, not the development UI');
  const built = await readFile(path.join(root, 'ui/dist/index.html'), 'utf8').catch(() => '');
  assert.equal(html, built, 'the binary must embed the current ui/dist; rebuild with make ui && make build');
  assert.match(served.headers.get('content-security-policy'), /script-src 'self';/);
  ok('the binary serves the current built reader under the production policy');

  browser = await chromium.launch({ headless: true, executablePath: option('--browser') });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: 'light' });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  await page.goto(origin);
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  await page.getByLabel('Email').fill('reader@example.localhost');
  await page.getByLabel('Password').fill('not the password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByRole('alert').waitFor();
  assert.equal(await page.evaluate(k => localStorage.getItem(k), key), null);
  ok('a wrong password shows an alert and stores nothing');
  await page.screenshot({ path: path.join(screenshots, 'sign-in-light.png') });

  const link = await cli(['open', '--no-browser']);
  assert(link.signInLink.startsWith(`${origin}/bundle/login#`));
  await page.goto(link.signInLink);
  await page.getByRole('button', { name: 'Sign out' }).waitFor();
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(new URL(page.url()).hash, '', 'the code never stays in the address bar');
  await page.getByText('reader@example.localhost', { exact: true }).waitFor();
  await page.getByText('A greeting from the CLI', { exact: true }).waitFor();
  ok('the single-use open link signs in and shows the account\'s greetings');
  const reused = await (await browser.newContext()).newPage();
  await reused.goto(link.signInLink);
  await reused.getByText(/used or expired/).waitFor();
  await reused.context().close();
  ok('the open link works only once');

  const messages = ['First synthetic hello', 'Second synthetic hello <b>bold</b>', 'Third greeting about telescopes'];
  for (const message of messages) {
    await page.getByLabel('New greeting').fill(message);
    await page.getByRole('button', { name: 'Add' }).click();
    await page.getByRole('list', { name: 'Greetings, newest first' }).getByText(message, { exact: true }).waitFor();
    await page.waitForTimeout(20); // Distinct creation times keep the order deterministic.
  }
  assert.equal(await page.getByLabel('New greeting').inputValue(), '');
  const listed = await page.getByRole('list', { name: 'Greetings, newest first' }).locator('li > p').allTextContents();
  assert.deepEqual(listed, [...messages].reverse().concat('A greeting from the CLI'), 'greetings are listed newest first');
  assert.equal(await page.locator('.greetings b').count(), 0, 'messages render as text');
  const cliList = await cli(['list']);
  assert.equal((cliList.rows ?? cliList).length, 4, 'the CLI sees the greetings added in the reader');
  ok('greetings added in the reader appear newest first as plain text');

  await page.getByRole('searchbox', { name: 'Search greetings' }).fill('synthetic hello');
  await page.getByRole('button', { name: 'Search' }).click();
  const results = page.getByRole('list', { name: 'Search results' });
  await results.waitFor();
  const found = await results.locator('li > p:first-child').allTextContents();
  assert.deepEqual(found.sort(), [messages[0], messages[1]].sort());
  await page.getByText(/2 matches for/).waitFor();
  await page.getByRole('searchbox', { name: 'Search greetings' }).fill('telescopes');
  await page.getByRole('button', { name: 'Search' }).click();
  await results.getByText(messages[2], { exact: true }).waitFor();
  await page.getByRole('searchbox', { name: 'Search greetings' }).fill('nonexistentword');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByText(/No greetings match/).waitFor();
  await page.getByRole('button', { name: 'Clear' }).click();
  ok('full-text search finds own greetings and reports no matches');
  await page.screenshot({ path: path.join(screenshots, 'reader-light.png'), fullPage: true });

  const lightBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: 'dark' });
  const darkBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  assert.notEqual(lightBackground, darkBackground, 'dark mode changes the palette');
  await page.screenshot({ path: path.join(screenshots, 'reader-dark.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 800 });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll at phone width');
  await page.screenshot({ path: path.join(screenshots, 'reader-mobile-dark.png'), fullPage: true });
  await page.emulateMedia({ colorScheme: 'light' });
  await page.setViewportSize({ width: 1280, height: 900 });
  ok('light, dark and phone layouts render');

  await page.getByRole('button', { name: 'Sign out' }).click();
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  assert.equal(await page.evaluate(k => localStorage.getItem(k), key), null);
  await page.reload();
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  ok('sign out clears the stored session and survives a reload');

  const code = await cli(['login-code', '--email', 'other@example.localhost']);
  await page.getByRole('button', { name: 'Use a sign-in code instead' }).click();
  await page.getByLabel('Sign-in code').fill(code.code);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByText('other@example.localhost', { exact: true }).waitFor();
  await page.getByText(/No greetings yet/).waitFor();
  await page.getByRole('searchbox', { name: 'Search greetings' }).fill('synthetic');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByText(/No greetings match/).waitFor();
  ok('a login code signs in another account, which sees and finds none of the first account\'s greetings');
  await page.getByRole('button', { name: 'Sign out' }).click();
  await page.getByLabel('Email').fill('reader@example.localhost');
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByText('Third greeting about telescopes', { exact: true }).waitFor();
  ok('password sign-in works');

  const unexpected = errors.filter(text => !/Failed to load resource: the server responded with a status of 400/.test(text));
  assert.deepEqual(unexpected, [], 'no runtime or security-policy errors');
  console.log('all reader browser checks passed');
} finally {
  await browser?.close();
  await cli(['stop']).catch(() => {});
  for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await Promise.all(children.map(child => child.exitCode === null && child.signalCode === null ? once(child, 'exit').catch(() => {}) : undefined));
  await rm(temporary, { recursive: true, force: true });
}
