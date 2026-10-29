import PocketBase, {
  BaseAuthStore,
  LocalAuthStore,
  type RecordAuthResponse,
  type SendOptions,
} from "pocketbase";

/** A greeting as the reader shows it. Reading it requires the project's account. */
export interface Greeting {
  id: string;
  message: string;
  created: string;
}
export interface SearchHit extends Greeting {
  score: number;
  excerpt: string;
}
export interface SearchPage {
  generation: string;
  hits: SearchHit[];
  hasMore: boolean;
}

// The SDK synchronizes this application's credentials across same-origin tabs.
// The single-use sign-in page (/bundle/login) writes the same key.
export const sessionKey = "templatecontext.reader.auth";
/**
 * The sign-in identity of the project's one account (bundle.AccountEmail in
 * internal/bundle/local.go). Nobody types or sees it: the reader asks only for
 * the password.
 */
export const ACCOUNT_EMAIL = "account@local.invalid";
function browserAuthStore(): BaseAuthStore {
  try {
    const probe = `${sessionKey}.probe.${Math.random()}`;
    window.localStorage.setItem(probe, "1");
    window.localStorage.removeItem(probe);
    return new LocalAuthStore(sessionKey);
  } catch {
    return new BaseAuthStore();
  }
}
const authStore = browserAuthStore();
// The SDK handles named keys, but localStorage.clear() emits a null key.
if (typeof window !== "undefined" && authStore instanceof LocalAuthStore) {
  window.addEventListener("storage", (event) => {
    if (event.storageArea !== window.localStorage) return;
    // Even an empty-to-empty logout must fence a sign-in that is still pending.
    if (event.key === null || event.key === sessionKey) cancelLogin();
    if (event.key === null) authStore.clear();
  });
}
if (authStore.record?.collectionName !== "users" || !authStore.isValid)
  authStore.clear();

/** Incremented whenever the signed-in identity changes, including sign-out. */
export let sessionGeneration = 0;
function identityKey() {
  return authStore.token
    ? `${authStore.record?.collectionName}:${authStore.record?.id}`
    : "";
}
let activeIdentity = identityKey();
let realtimeCleanup: Promise<void> = Promise.resolve();
authStore.onChange(() => {
  const next = identityKey();
  if (next === activeIdentity) {
    // A sibling tab already renewed this identity; do not renew it again on focus.
    if (next) {
      refreshedAt = Date.now();
      refreshedToken = authStore.token;
    }
    return;
  }
  activeIdentity = next;
  sessionGeneration++;
  realtimeCleanup = realtimeCleanup
    .then(() => pb.realtime.unsubscribe())
    .catch(() => {});
});

export const pb = new PocketBase(
  typeof window === "undefined" ? "http://localhost" : window.location.origin,
  authStore,
);
// Concurrent list and search reads share an endpoint but must not cancel each other.
pb.autoCancellation(false);
// Reject fully parsed responses from a previous identity before callers see them.
const send = pb.send.bind(pb);
pb.send = async <T = unknown>(
  path: string,
  options: SendOptions = {},
): Promise<T> => {
  const generation = sessionGeneration;
  const token = authStore.token;
  const identity = identityKey();
  try {
    const result = await send<T>(path, options);
    if (generation !== sessionGeneration || identity !== identityKey())
      throw new Error("Session changed");
    return result;
  } catch (error) {
    const status = (error as { status?: number }).status;
    if (
      generation === sessionGeneration &&
      token &&
      token === authStore.token &&
      (status === 401 || status === 403)
    )
      authStore.clear();
    throw error;
  }
};

export class TruncatedQueryError extends Error {
  constructor() {
    super(
      "The response exceeded the query limit. Narrow the request and retry.",
    );
  }
}
const validID = (value: unknown): value is string =>
  typeof value === "string" && /^[a-z0-9]{15}$/.test(value);
/** Quote a record id for SQL; anything else is refused before it reaches SQL. */
function identity(value: unknown): string {
  if (!validID(value)) throw new Error("Invalid record identity");
  return `'${value}'`;
}

interface SQLResult {
  columns: string[];
  rows: unknown[][];
  truncated: boolean;
}
/** Authenticated read-only SQL over the server's readable tables and columns. */
export async function query<T>(sql: string): Promise<T[]> {
  const generation = sessionGeneration;
  try {
    const result = await pb.send<SQLResult>("/api/context/query", {
      method: "POST",
      body: { sql },
    });
    if (generation !== sessionGeneration) throw new Error("Session changed");
    if (result.truncated) throw new TruncatedQueryError();
    return result.rows.map(
      (row) =>
        Object.fromEntries(
          result.columns.map((column, index) => [column, row[index]]),
        ) as T,
    );
  } catch (error) {
    if ((error as { status?: number })?.status === 413)
      throw new TruncatedQueryError();
    throw error;
  }
}

function requireSession(): void {
  if (!pb.authStore.isValid || !pb.authStore.record?.id)
    throw new Error("Please sign in to continue");
}
function greeting(row: Partial<Greeting>): Greeting {
  if (
    !validID(row.id) ||
    typeof row.message !== "string" ||
    typeof row.created !== "string"
  )
    throw new Error("Invalid greeting");
  return { id: row.id, message: row.message, created: row.created };
}

export const LIST_LIMIT = 100;
/** The greetings, newest first. */
export async function listGreetings(limit = LIST_LIMIT): Promise<Greeting[]> {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 200)
    throw new Error("Invalid list size");
  requireSession();
  const rows = await query<Greeting>(
    `SELECT id, message, created FROM greetings ORDER BY created DESC, id DESC LIMIT ${limit}`,
  );
  return rows.map(greeting);
}

export const MESSAGE_LIMIT = 500;
export function validMessage(message: string): string {
  const trimmed = message.trim();
  if (!trimmed) throw new Error("Write a greeting first");
  if (trimmed.length > MESSAGE_LIMIT)
    throw new Error(`A greeting has at most ${MESSAGE_LIMIT} characters`);
  return trimmed;
}
/** Create a greeting. */
export async function addGreeting(message: string): Promise<Greeting> {
  requireSession();
  const record = await pb
    .collection("greetings")
    .create<Greeting>({ message: validMessage(message) });
  return greeting(record);
}

/** Notify on any change to the greetings. Returns an unsubscribe. */
export async function watchGreetings(notify: () => void): Promise<() => void> {
  const generation = sessionGeneration;
  const unsubscribe = await pb.collection("greetings").subscribe("*", () => {
    if (generation === sessionGeneration) notify();
  });
  return () => void unsubscribe().catch(() => {});
}

export class SearchChangedError extends Error {
  constructor() {
    super("Greetings changed. Search again to see consistent results.");
  }
}
export class SearchUnavailableError extends Error {
  constructor(readonly reason: "too-large" | "unavailable") {
    super(
      {
        "too-large": "There are too many greetings to search right now.",
        unavailable: "Search is temporarily unavailable. Please retry shortly.",
      }[reason],
    );
  }
}
export const SEARCH_INDEX = "greetings";
export function validSearch(term: string): string {
  const trimmed = term.trim();
  if (
    !trimmed ||
    term.length > 500 ||
    term.includes("\0") ||
    trimmed.split(/\s+/).length > 16
  )
    throw new Error(
      "Search must contain 1 to 500 characters and at most 16 words",
    );
  return trimmed;
}
/**
 * Full-text search over the greetings through the vendored search endpoint.
 * The index has no scope; paging past the first page pins the generation so
 * results stay consistent.
 */
export async function searchGreetings(
  term: string,
  offset = 0,
  limit = 20,
  expectedGeneration?: string,
): Promise<SearchPage> {
  if (
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    offset > 10000 ||
    !Number.isSafeInteger(limit) ||
    limit < 1 ||
    limit > 100 ||
    (offset > 0 && !expectedGeneration)
  )
    throw new Error("Invalid search page");
  const text = validSearch(term);
  requireSession();
  const session = sessionGeneration;
  const active = () => {
    if (session !== sessionGeneration || !pb.authStore.isValid)
      throw new Error("Session changed");
  };
  let result: {
    index: string;
    scope?: string;
    generation: string;
    hits: Array<{ id: string; score: number; excerpt: string }>;
    hasMore: boolean;
    truncated: boolean;
  };
  try {
    result = await pb.send("/api/context/search", {
      method: "POST",
      body: {
        index: SEARCH_INDEX,
        query: text,
        offset,
        limit,
        ...(expectedGeneration ? { expectedGeneration } : {}),
      },
    });
  } catch (error) {
    const status = (error as { status?: number })?.status;
    if (status === 409) throw new SearchChangedError();
    if (status === 413) throw new SearchUnavailableError("too-large");
    if (status === 503 || status === 429)
      throw new SearchUnavailableError("unavailable");
    throw error;
  }
  active();
  if (
    result.index !== SEARCH_INDEX ||
    (result.scope !== undefined && result.scope !== "") ||
    typeof result.generation !== "string" ||
    !/^[a-f0-9]{64}$/.test(result.generation) ||
    (expectedGeneration && result.generation !== expectedGeneration)
  )
    throw new SearchChangedError();
  if (
    !Array.isArray(result.hits) ||
    result.hits.length > limit ||
    typeof result.hasMore !== "boolean" ||
    result.hasMore !== result.truncated ||
    (result.hasMore && !result.hits.length)
  )
    throw new Error("Invalid search response");
  const seen = new Set<string>();
  result.hits.forEach((hit, n) => {
    if (
      !validID(hit.id) ||
      seen.has(hit.id) ||
      !Number.isFinite(hit.score) ||
      typeof hit.excerpt !== "string"
    )
      throw new Error("Invalid search hit");
    seen.add(hit.id);
    const previous = result.hits[n - 1];
    if (
      previous &&
      (previous.score > hit.score ||
        (previous.score === hit.score && previous.id >= hit.id))
    )
      throw new Error("Invalid search ranking");
  });
  if (!result.hits.length)
    return { generation: result.generation, hits: [], hasMore: result.hasMore };
  // Hydrate fixed columns by validated ids; search text never becomes SQL.
  const ids = result.hits.map((hit) => identity(hit.id)).join(",");
  const rows = (
    await query<Greeting>(
      `SELECT id, message, created FROM greetings WHERE id IN (${ids}) ORDER BY id LIMIT ${limit}`,
    )
  ).map(greeting);
  active();
  const byID = new Map(rows.map((row) => [row.id, row]));
  // A greeting deleted between the search and this read is a changed result set.
  if (byID.size !== result.hits.length) throw new SearchChangedError();
  return {
    generation: result.generation,
    hasMore: result.hasMore,
    hits: result.hits.map((hit) => ({
      ...byID.get(hit.id)!,
      score: hit.score,
      excerpt: hit.excerpt,
    })),
  };
}

let pendingSignIn: { cancel: (message?: string) => void } | null = null;
/**
 * Run one sign-in exchange on an isolated client, then save its result only if
 * nothing changed meanwhile: no cancellation, newer attempt, sign-out or other
 * account (in this or another tab).
 */
async function signIn(
  exchange: (client: PocketBase) => Promise<RecordAuthResponse>,
): Promise<void> {
  pendingSignIn?.cancel();
  const generation = sessionGeneration,
    token = authStore.token;
  const client = new PocketBase(pb.baseURL, new BaseAuthStore());
  client.autoCancellation(false);
  let ended = false;
  let rejectAttempt: (error: Error) => void = () => {};
  const attempt = {
    cancel(message = "Sign-in cancelled. You can try again.") {
      if (ended) return;
      ended = true;
      // This isolated client owns only this attempt.
      client.cancelAllRequests();
      rejectAttempt(Object.assign(new Error(message), { isAbort: true }));
    },
  };
  pendingSignIn = attempt;
  try {
    const result = await new Promise<RecordAuthResponse>((resolve, reject) => {
      rejectAttempt = reject;
      exchange(client).then(resolve, reject);
    });
    if (
      ended ||
      pendingSignIn !== attempt ||
      generation !== sessionGeneration ||
      token !== authStore.token
    )
      throw new Error("Session changed");
    if (
      typeof result?.token !== "string" ||
      !result.token ||
      result.record?.collectionName !== "users"
    )
      throw new Error("Invalid account identity");
    refreshedAt = Date.now();
    refreshedToken = result.token;
    authStore.save(result.token, result.record);
  } finally {
    ended = true;
    if (pendingSignIn === attempt) pendingSignIn = null;
  }
}
/** Cancel a pending sign-in and fence late responses. */
export function cancelLogin(): void {
  pendingSignIn?.cancel();
}
/** Sign in to the project's one account with its password. */
export async function loginPassword(password: string): Promise<void> {
  await signIn((client) =>
    client.collection("users").authWithPassword(ACCOUNT_EMAIL, password),
  );
}
/** Normalize a one-time code from `templatecontext login-code` or an `open` link. */
export function normalizeCode(input: string): string {
  let code = input.trim();
  // Accept a whole pasted sign-in link; only its fragment is the code.
  const hash = code.indexOf("#");
  if (hash >= 0) code = code.slice(hash + 1);
  if (!/^[A-Za-z0-9_-]{8,256}$/.test(code))
    throw new Error("Enter the sign-in code exactly as it was given to you");
  return code;
}
/** Redeem a single-use sign-in code (no password passes through anyone else). */
export async function loginCode(input: string): Promise<void> {
  const code = normalizeCode(input);
  await signIn((client) =>
    client.send<RecordAuthResponse>("/api/bundle/redeem", {
      method: "POST",
      body: { code },
    }),
  );
}

let refreshedAt = 0;
let refreshedToken = "";
/** Renew the token at most every five minutes; a rejected token signs out. */
export async function refreshSession(): Promise<void> {
  const initiatingToken = pb.authStore.token;
  if (!initiatingToken) throw new Error("Please sign in to continue");
  const generation = sessionGeneration;
  if (initiatingToken === refreshedToken && Date.now() - refreshedAt < 300000)
    return;
  // Save explicitly: the SDK authRefresh helper saves even after a user signs out.
  const result = await pb.send<RecordAuthResponse>(
    "/api/collections/users/auth-refresh",
    { method: "POST", headers: { Authorization: initiatingToken } },
  );
  if (
    pb.authStore.token !== initiatingToken ||
    generation !== sessionGeneration
  )
    return;
  if (result.record.collectionName !== "users")
    throw new Error("Invalid account identity");
  refreshedAt = Date.now();
  refreshedToken = result.token;
  pb.authStore.save(result.token, result.record);
}
export function logout(): void {
  cancelLogin();
  pb.authStore.clear();
}

export const api = {
  cancelLogin,
  loginPassword,
  loginCode,
  refreshSession,
  logout,
  listGreetings,
  addGreeting,
  watchGreetings,
  searchGreetings,
};
