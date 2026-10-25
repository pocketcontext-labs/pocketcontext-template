import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { test } from "node:test";
import {
  assertPortAvailable,
  firstTunnelAddress,
  parseOptions,
  projectOptions,
  run,
  tunnelInstructions,
} from "../scripts/dev.mjs";

const address = "tc1-synthetic-address-for-tests";

test("launcher requires an explicit database and valid port", () => {
  assert.throws(() => parseOptions([]), /explicit development database/);
  assert.throws(
    () => parseOptions(["--db", "--binary", "bin/templatecontext"]),
    /Usage/,
  );
  assert.throws(
    () => parseOptions(["--db", "example.db", "--vite-port", "0"]),
    /Port/,
  );
  assert.throws(
    () => parseOptions(["--db", "example.db", "--take-over"]),
    /Usage/,
  );
});

test("launcher parses --tunnel as a flag and --email only with it", () => {
  assert.equal(parseOptions(["--db", "example.db"]).tunnel, false);
  const options = parseOptions(["--tunnel", "--db", "example.db", "--email", "You@Example.localhost"]);
  assert.equal(options.tunnel, true);
  assert.equal(options.email, "you@example.localhost");
  assert.equal(parseOptions(["--db", "example.db", "--tunnel"]).tunnel, true);
  assert.throws(() => parseOptions(["--db", "example.db", "--tunnel", "--tunnel"]), /Usage/);
  assert.throws(() => parseOptions(["--db", "example.db", "--tunnel", "yes"]), /Usage/);
  assert.throws(() => parseOptions(["--db", "example.db", "--email", "you@example.localhost"]), /--tunnel/);
  assert.throws(() => parseOptions(["--db", "example.db", "--tunnel", "--email", "nobody"]), /--email/);
  assert.throws(() => parseOptions(["--db", "example.db", "--tunnel", "--email"]), /Usage/);
});

test("launcher reads only explicit project settings and refuses missing databases", async () => {
  const folder = await mkdtemp(join(tmpdir(), "templatecontext-launcher-"));
  try {
    const db = join(folder, "templatecontext.db");
    await assert.rejects(
      projectOptions(parseOptions(["--db", db])),
      /Initialize/,
    );
    await writeFile(db, "synthetic");
    assert.equal((await projectOptions(parseOptions(["--db", folder]))).db, db);
    assert.equal((await projectOptions(parseOptions(["--db", db]))).port, 8098);
    await assert.rejects(projectOptions(parseOptions(["--db", db, "--tunnel"])), /--email/);
    await writeFile(
      join(folder, "templatecontext.json"),
      JSON.stringify({ port: 18123, email: "dev@example.localhost" }),
    );
    const options = await projectOptions(parseOptions(["--db", db, "--tunnel"]));
    assert.equal(options.port, 18123);
    assert.equal(options.email, "dev@example.localhost");
    assert.equal(
      (await projectOptions(parseOptions(["--db", db, "--tunnel", "--email", "you@example.localhost"]))).email,
      "you@example.localhost",
    );
    await assert.rejects(
      projectOptions(parseOptions(["--db", db, "--vite-port", "18123"])),
      /different ports/,
    );
    await writeFile(
      join(folder, "templatecontext.json"),
      JSON.stringify({ port: "18123" }),
    );
    await assert.rejects(projectOptions(parseOptions(["--db", db])), /Port/);
    await writeFile(join(folder, "templatecontext.json"), "broken JSON");
    await assert.rejects(
      projectOptions(parseOptions(["--db", db])),
      SyntaxError,
    );
  } finally {
    await rm(folder, { recursive: true, force: true });
  }
});

test("launcher refuses an occupied port without touching its listener", async () => {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  try {
    await assert.rejects(assertPortAvailable(port), /already in use/);
    assert.equal(server.listening, true);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
  await assertPortAvailable(port);
});

test("tunnel instructions name the same port, the stdin forward and the code", () => {
  const text = tunnelInstructions({ address, port: 18123, code: "c0de0123456789", expires: "2026-10-25T10:10:00Z", email: "you@example.localhost" });
  assert.match(text, new RegExp(`Tunnel address: ${address}`));
  assert(text.includes(`printf '%s\\n' "$ADDRESS" | templatecontext tunnel forward --address-stdin --url http://127.0.0.1:18123`));
  assert.equal(text.split(address).length, 2, "the address appears exactly once");
  assert.match(text, /must use the same port, 18123/);
  assert.match(text, /Host 127\.0\.0\.1:18123/);
  assert.match(text, /ws:\/\/127\.0\.0\.1:18123/);
  assert.match(text, /read -r ADDRESS/);
  assert.match(text, /Sign-in code for you@example\.localhost \(single use, expires 2026-10-25T10:10:00Z\)/);
  assert.match(text, /^ {2}c0de0123456789$/m);
  assert(text.includes("http://127.0.0.1:18123/bundle/login#c0de0123456789"));
  assert.doesNotMatch(tunnelInstructions({ address, port: 18123 }), /Sign-in code/);
});

test("the first address line wins; other output is ignored", async () => {
  const stream = new PassThrough();
  const seen = [];
  const first = firstTunnelAddress(stream, (data) => seen.push(data.address));
  stream.write("not json\n");
  stream.write(`${JSON.stringify({ warning: "no address yet" })}\n`);
  stream.write(`${JSON.stringify({ address, port: 18123 })}\n`);
  stream.end(`${JSON.stringify({ address: "second" })}\n`);
  assert.equal((await first).address, address);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(seen, [address, "second"]);
  const empty = new PassThrough();
  const none = firstTunnelAddress(empty);
  empty.end("{}\n");
  await assert.rejects(none, /without an address/);
});

/** A project folder and fake child processes; nothing real is started. */
async function harness({ tunnelLines = [JSON.stringify({ address, port: 18123, serverRunning: true })], loginCode = { code: "c0de0123456789", email: "dev@example.localhost", expires: "2026-10-25T10:10:00Z" }, tunnelExits = false } = {}) {
  const folder = await mkdtemp(join(tmpdir(), "templatecontext-launcher-run-"));
  const db = join(folder, "templatecontext.db");
  await writeFile(db, "synthetic");
  await writeFile(join(folder, "templatecontext.json"), JSON.stringify({ port: 18123, email: "dev@example.localhost", instance: "instance-1" }));
  const events = [];
  const children = [];
  let pid = 1000;
  let backend;
  function fakeChild(argv) {
    const child = new EventEmitter();
    child.pid = ++pid;
    child.argv = argv;
    child.stdout = new PassThrough();
    child.killed = [];
    child.exit = (code, signal = null) => {
      if (child.ended) return;
      child.ended = true;
      child.stdout.end();
      setImmediate(() => child.emit("exit", code, signal));
    };
    child.kill = (signal) => {
      child.killed.push(signal);
      events.push(`kill ${argv[0]} ${signal}`);
      child.exit(null, signal);
    };
    children.push(child);
    return child;
  }
  const spawn = (command, argv, options) => {
    const isVite = command === process.execPath;
    const child = fakeChild(isVite ? ["vite", ...argv.slice(1)] : argv);
    child.options = options;
    const name = isVite ? "vite" : argv[0] === "tunnel" ? "tunnel" : argv[0];
    events.push(`spawn ${name}`);
    setImmediate(() => {
      if (name === "serve") backend = child;
      if (name === "tunnel") {
        for (const line of tunnelLines) child.stdout.write(`${line}\n`);
        if (tunnelExits) child.exit(1);
      }
      if (name === "login-code") {
        if (loginCode) child.stdout.end(JSON.stringify(loginCode));
        child.exit(loginCode ? 0 : 1);
      }
      if (name === "stop") {
        child.exit(0);
        backend?.exit(0);
      }
    });
    return child;
  };
  const fetch = async (url) => {
    if (String(url).endsWith("/api/bundle/instance")) {
      if (!backend) throw new Error("not listening");
      return new Response(JSON.stringify({ app: "templatecontext", instance: "instance-1", pid: backend.pid, uiDev: "http://127.0.0.1:5173" }));
    }
    return new Response("vite");
  };
  const output = [];
  const signals = new EventEmitter();
  const deps = {
    spawn, fetch, signals,
    access: async () => {},
    assertPortAvailable: async () => {},
    log: (text) => output.push(text),
    tunnelTimeout: 2000,
  };
  return {
    db, events, children, output, signals, deps,
    text: () => output.join("\n"),
    cleanup: () => rm(folder, { recursive: true, force: true }),
  };
}
async function waitFor(check, what) {
  const deadline = Date.now() + 5000;
  while (!check()) {
    if (Date.now() > deadline) throw new Error(`Timed out: ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

test("--tunnel runs tunnel serve, prints the address once with a code, and Ctrl-C stops everything", async () => {
  const repeated = JSON.stringify({ address, port: 18123, serverRunning: true });
  const h = await harness({ tunnelLines: [repeated, repeated] });
  try {
    const running = run(["--db", h.db, "--tunnel"], h.deps);
    await waitFor(() => h.text().includes("Edit ui/src"), "instructions");
    const tunnel = h.children.find((child) => child.argv[0] === "tunnel");
    assert.deepEqual(tunnel.argv, ["tunnel", "serve", "--db", h.db]);
    assert.deepEqual(tunnel.options.stdio, ["ignore", "pipe", "inherit"], "the address is read from stdout, never logged by the launcher elsewhere");
    const issued = h.children.find((child) => child.argv[0] === "login-code");
    assert.deepEqual(issued.argv, ["login-code", "--db", h.db, "--email", "dev@example.localhost"]);
    const vite = h.children.find((child) => child.argv[0] === "vite");
    assert.equal(vite.options.env.TEMPLATECONTEXT_DEV_PORT, "18123");
    const text = h.text();
    assert.equal(text.split(address).length, 2, "a repeated address line is printed once");
    assert(text.includes(`printf '%s\\n' "$ADDRESS" | templatecontext tunnel forward --address-stdin --url http://127.0.0.1:18123`));
    assert.match(text, /must use the same port, 18123/);
    assert.match(text, /^ {2}c0de0123456789$/m);
    assert.match(text, /including the tunnel/);
    h.signals.emit("SIGINT");
    await running;
    assert.deepEqual(h.events.filter((event) => event.startsWith("spawn")), ["spawn vite", "spawn serve", "spawn tunnel", "spawn login-code", "spawn stop"]);
    assert(h.events.indexOf("kill tunnel SIGTERM") < h.events.indexOf("spawn stop"), "the tunnel stops before the server");
    assert(h.events.includes("kill vite SIGTERM"));
    assert(h.children.every((child) => child.ended), "no child process survives Ctrl-C");
    assert.equal(h.signals.listenerCount("SIGINT"), 0);
  } finally {
    await h.cleanup();
  }
});

test("without --tunnel no tunnel or code is started", async () => {
  const h = await harness();
  try {
    const running = run(["--db", h.db], h.deps);
    await waitFor(() => h.text().includes("Edit ui/src"), "ready");
    h.signals.emit("SIGTERM");
    await running;
    assert.deepEqual(h.events.filter((event) => event.startsWith("spawn")), ["spawn vite", "spawn serve", "spawn stop"]);
    assert.doesNotMatch(h.text(), /Tunnel/);
  } finally {
    await h.cleanup();
  }
});

test("a failed login code still prints the address and how to issue a code", async () => {
  const h = await harness({ loginCode: null });
  try {
    const running = run(["--db", h.db, "--tunnel"], h.deps);
    await waitFor(() => h.text().includes("Edit ui/src"), "ready");
    assert.match(h.text(), /Could not issue a sign-in code/);
    assert.match(h.text(), new RegExp(address));
    assert.doesNotMatch(h.text(), /Sign-in code for/);
    h.signals.emit("SIGINT");
    await running;
  } finally {
    await h.cleanup();
  }
});

test("a tunnel that stops without an address fails the launcher and stops the others", async () => {
  const h = await harness({ tunnelLines: [], tunnelExits: true });
  try {
    await assert.rejects(run(["--db", h.db, "--tunnel"], h.deps), /Tunnel stopped before it reported an address/);
    assert(h.children.every((child) => child.ended));
    assert(h.events.includes("spawn stop"), "the server is stopped through its authenticated endpoint");
  } finally {
    await h.cleanup();
  }
});
