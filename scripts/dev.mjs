#!/usr/bin/env node
import { spawn as spawnProcess } from "node:child_process";
import { access as accessPath, readFile, stat } from "node:fs/promises";
import { createServer } from "node:net";
import { dirname, join, resolve } from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const root = fileURLToPath(new URL("../", import.meta.url));
export const DEFAULT_PORT = 8098;
const usage =
  "Usage: node scripts/dev.mjs --db PATH [--binary PATH] [--vite-port 5173] [--tunnel [--email EMAIL]]";
const valued = ["--db", "--binary", "--vite-port", "--email"];
const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function parseOptions(args) {
  const options = {
    binary: join(
      root,
      "bin",
      process.platform === "win32" ? "templatecontext.exe" : "templatecontext",
    ),
    vitePort: 5173,
    tunnel: false,
  };
  for (let i = 0; i < args.length; i++) {
    const name = args[i];
    if (name === "--tunnel") {
      if (options.tunnel) throw new Error(usage);
      options.tunnel = true;
      continue;
    }
    if (
      !valued.includes(name) ||
      !args[i + 1] ||
      args[i + 1].startsWith("--")
    ) {
      throw new Error(usage);
    }
    const value = args[++i];
    if (name === "--vite-port") options.vitePort = validPort(Number(value));
    else if (name === "--email") {
      if (!emailPattern.test(value))
        throw new Error("--email must look like name@example.localhost");
      options.email = value.toLowerCase();
    } else options[name.slice(2)] = resolve(value);
  }
  if (!options.db)
    throw new Error(`An explicit development database is required. ${usage}`);
  if (options.email && !options.tunnel)
    throw new Error(
      `--email selects the account for the --tunnel sign-in code. ${usage}`,
    );
  return options;
}

function validPort(value) {
  if (!Number.isInteger(value) || value < 1024 || value > 65535)
    throw new Error("Port must be 1024-65535");
  return value;
}

export async function projectOptions(options) {
  const info = await stat(options.db).catch((error) => {
    if (error.code === "ENOENT")
      throw new Error(
        "Initialize an explicit development database with templatecontext init --db PATH first.",
      );
    throw error;
  });
  const db = info.isDirectory()
    ? join(options.db, "templatecontext.db")
    : options.db;
  await accessPath(db);
  let config = {};
  try {
    config = JSON.parse(
      await readFile(join(dirname(db), "templatecontext.json"), "utf8"),
    );
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  const port = validPort(
    config.port === undefined || config.port === 0 ? DEFAULT_PORT : config.port,
  );
  if (port === options.vitePort)
    throw new Error("TemplateContext and Vite must use different ports");
  const email =
    options.email ??
    (typeof config.email === "string" && config.email
      ? config.email
      : undefined);
  if (options.tunnel && !email)
    throw new Error(
      "--tunnel issues a sign-in code: pass --email EMAIL or set the account email in templatecontext.json.",
    );
  return { ...options, db, port, ...(email ? { email } : {}) };
}

export async function assertPortAvailable(port) {
  await new Promise((resolvePromise, reject) => {
    const server = createServer();
    server.once("error", () =>
      reject(
        new Error(
          `Port ${port} is already in use. Stop its server explicitly or choose another port.`,
        ),
      ),
    );
    server.listen(port, "127.0.0.1", () => server.close(resolvePromise));
  });
}

/**
 * Instructions for the person on the other computer. The address is printed
 * here, on this terminal only; it is never written to a log or file.
 */
export function tunnelInstructions({ address, port, code, expires, email }) {
  const url = `http://127.0.0.1:${port}`;
  const lines = [
    "",
    "Tunnel ready. Keep this address private: anyone with it can reach this port (sign-in is still required).",
    "",
    `  Tunnel address: ${address}`,
    "",
    "On the other computer, store the address without putting it in shell history (run `read -r ADDRESS` and paste it), then run:",
    "",
    `  printf '%s\\n' "$ADDRESS" | templatecontext tunnel forward --address-stdin --url ${url}`,
    "",
    `The forward must use the same port, ${port}. The server accepts only the Host 127.0.0.1:${port} or localhost:${port}, and the development security policy pins the hot-reload WebSocket to ws://127.0.0.1:${port}; a forward on any other port is refused.`,
  ];
  if (code) {
    lines.push(
      "",
      `Sign-in code for ${email} (single use${expires ? `, expires ${expires}` : ""}):`,
      "",
      `  ${code}`,
      "",
      `With the forward running, open ${url}/bundle/login#${code} there, or choose "Use a sign-in code instead" at ${url} and paste the code.`,
      `For a new code run: templatecontext login-code --db PATH --email ${email}`,
    );
  }
  lines.push("");
  return lines.join("\n");
}

/** Resolve with the first JSON line from tunnel serve that carries an address. */
export function firstTunnelAddress(stream, onLine = () => {}) {
  return new Promise((resolvePromise, reject) => {
    const lines = createInterface({ input: stream });
    let found = false;
    lines.on("line", (line) => {
      let data;
      try {
        data = JSON.parse(line);
      } catch {
        return;
      }
      if (typeof data?.address !== "string" || !data.address) return;
      onLine(data);
      if (!found) {
        found = true;
        resolvePromise(data);
      }
    });
    lines.once("close", () => {
      if (!found) reject(new Error("tunnel serve stopped without an address"));
    });
  });
}

function starter(spawn) {
  return function start(command, args, options = {}) {
    const child = spawn(command, args, { stdio: "inherit", ...options });
    const state = { child, done: false };
    state.exited = new Promise((resolvePromise) => {
      child.once("error", (error) => {
        state.done = true;
        resolvePromise(error.message);
      });
      child.once("exit", (code, signal) => {
        state.done = true;
        resolvePromise(signal || `exit ${code}`);
      });
    });
    return state;
  };
}

async function waitReady(fetch, url, state, signal, check) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    signal.throwIfAborted();
    if (state.done)
      throw new Error(
        `Process stopped before readiness: ${await state.exited}`,
      );
    try {
      const response = await fetch(url, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(1000)]),
      });
      const ready = response.ok && (await check(response));
      if (!response.bodyUsed) await response.body?.cancel();
      if (ready && !state.done) return;
    } catch (error) {
      if (signal.aborted) throw error;
    }
    await delay(150, undefined, { signal });
  }
  throw new Error(`Server did not become ready within 60 seconds: ${url}`);
}

async function terminate(state, timeout = 5000) {
  if (!state || state.done) return;
  state.child.kill("SIGTERM");
  await Promise.race([state.exited, delay(timeout, undefined, { ref: false })]);
  if (!state.done) {
    state.child.kill("SIGKILL");
    await state.exited;
  }
}

async function collect(state) {
  let output = "";
  state.child.stdout.setEncoding("utf8");
  state.child.stdout.on("data", (chunk) => {
    output += chunk;
  });
  const result = await state.exited;
  if (result !== "exit 0") throw new Error(result);
  return output;
}

const defaults = {
  spawn: spawnProcess,
  fetch: (...args) => globalThis.fetch(...args),
  access: accessPath,
  assertPortAvailable,
  log: (text) => console.log(text),
  signals: process,
  tunnelTimeout: 90_000,
};

export async function run(args, overrides = {}) {
  const deps = { ...defaults, ...overrides };
  const start = starter(deps.spawn);
  const options = await projectOptions(parseOptions(args));
  const vite = join(root, "ui", "node_modules", "vite", "bin", "vite.js");
  await deps.access(options.binary).catch(() => {
    throw new Error(
      `Build TemplateContext once with make build; executable missing: ${options.binary}`,
    );
  });
  await deps.access(vite).catch(() => {
    throw new Error(
      "Install frontend dependencies first: pnpm --dir ui install --frozen-lockfile",
    );
  });
  await deps.assertPortAvailable(options.port);
  await deps.assertPortAvailable(options.vitePort);
  const controller = new AbortController();
  let finish;
  const interrupted = new Promise((resolvePromise) => {
    finish = resolvePromise;
  });
  const onSignal = () => {
    controller.abort();
    finish();
  };
  deps.signals.on("SIGINT", onSignal);
  deps.signals.on("SIGTERM", onSignal);
  let frontend;
  let backend;
  let tunnel;
  let backendReady = false;
  try {
    frontend = start(
      process.execPath,
      [vite, "--port", String(options.vitePort)],
      {
        cwd: join(root, "ui"),
        env: { ...process.env, TEMPLATECONTEXT_DEV_PORT: String(options.port) },
      },
    );
    const upstream = `http://127.0.0.1:${options.vitePort}`;
    await waitReady(
      deps.fetch,
      upstream,
      frontend,
      controller.signal,
      async () => true,
    );
    backend = start(
      options.binary,
      ["serve", "--db", options.db, "--ui-dev", upstream],
      { cwd: root },
    );
    const origin = `http://127.0.0.1:${options.port}`;
    await waitReady(
      deps.fetch,
      `${origin}/api/bundle/instance`,
      backend,
      controller.signal,
      async (response) => {
        const instance = await response.json();
        const config = JSON.parse(
          await readFile(
            join(dirname(options.db), "templatecontext.json"),
            "utf8",
          ),
        );
        return (
          instance.app === "templatecontext" &&
          instance.instance === config.instance &&
          instance.pid === backend.child.pid &&
          instance.uiDev === upstream
        );
      },
    );
    backendReady = true;
    deps.log(
      `\nReader ready at ${origin}. Sign in there, or run templatecontext open --db PATH in another terminal.`,
    );
    const stopped = [
      interrupted,
      frontend.exited.then((value) => `Vite stopped (${value})`),
      backend.exited.then((value) => `TemplateContext stopped (${value})`),
    ];
    if (options.tunnel) {
      // stdout carries the address: read it here, print it once to this
      // terminal and nowhere else. stderr stays on the terminal.
      tunnel = start(options.binary, ["tunnel", "serve", "--db", options.db], {
        cwd: root,
        stdio: ["ignore", "pipe", "inherit"],
      });
      stopped.push(tunnel.exited.then((value) => `Tunnel stopped (${value})`));
      let printed = "";
      const ready = firstTunnelAddress(tunnel.child.stdout, (data) => {
        if (printed && data.address !== printed) {
          printed = data.address;
          deps.log(
            `\nThe tunnel address changed. New address: ${data.address}\n`,
          );
        }
      }).catch(() => new Promise(() => {})); // The exit below reports its status.
      const failed = Promise.race([
        tunnel.exited.then(
          (value) => `Tunnel stopped before it reported an address (${value})`,
        ),
        delay(deps.tunnelTimeout, undefined, {
          ref: false,
          signal: controller.signal,
        }).then(
          () =>
            `The tunnel did not report an address within ${deps.tunnelTimeout / 1000} seconds; check the network`,
        ),
      ]).then((reason) => {
        throw new Error(reason);
      });
      const served = await Promise.race([ready, failed, interrupted]);
      if (controller.signal.aborted || !served) return;
      printed = served.address;
      let code;
      try {
        const issued = start(
          options.binary,
          ["login-code", "--db", options.db, "--email", options.email],
          { cwd: root, stdio: ["ignore", "pipe", "inherit"] },
        );
        code = JSON.parse(await collect(issued));
        if (typeof code?.code !== "string" || !code.code)
          throw new Error("no code in the response");
      } catch (error) {
        code = undefined;
        deps.log(
          `Could not issue a sign-in code (${error.message}). Run templatecontext login-code --db PATH --email ${options.email}.`,
        );
      }
      if (controller.signal.aborted) return;
      deps.log(
        tunnelInstructions({
          address: served.address,
          port: options.port,
          code: code?.code,
          expires: code?.expires,
          email: code?.email ?? options.email,
        }),
      );
    }
    deps.log(
      `Edit ui/src for hot updates. Press Ctrl-C to stop all development processes${options.tunnel ? ", including the tunnel" : ""}.\n`,
    );
    const result = await Promise.race(stopped);
    if (result) throw new Error(result);
  } catch (error) {
    if (!controller.signal.aborted) throw error;
  } finally {
    // The tunnel goes first so nobody reaches a server that is shutting down.
    await terminate(tunnel);
    if (backendReady && !backend.done) {
      // The authenticated stop endpoint flushes snapshots on Windows as well as Unix.
      const stop = start(options.binary, ["stop", "--db", options.db], {
        stdio: "ignore",
      });
      await Promise.race([
        stop.exited,
        delay(35_000, undefined, { ref: false }),
      ]);
      await terminate(stop);
      await Promise.race([
        backend.exited,
        delay(5000, undefined, { ref: false }),
      ]);
    }
    await terminate(backend, 35_000);
    await terminate(frontend);
    deps.signals.off("SIGINT", onSignal);
    deps.signals.off("SIGTERM", onSignal);
  }
}

if (
  process.argv[1] &&
  pathToFileURL(resolve(process.argv[1])).href === import.meta.url
) {
  if (process.argv.slice(2).includes("--help")) console.log(usage);
  else
    run(process.argv.slice(2)).catch((error) => {
      console.error(error.message);
      process.exitCode = 1;
    });
}
