import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  addGreeting,
  listGreetings,
  logout,
  pb,
  query,
  refreshSession,
  searchGreetings,
  SearchChangedError,
  SearchUnavailableError,
  TruncatedQueryError,
  validMessage,
  watchGreetings,
} from "./api";

const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users" };
const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const generation = "a".repeat(64);
function response(rows: Record<string, unknown>[], truncated = false) {
  const columns = ["id", "message", "created"];
  return { columns, rows: rows.map(row => columns.map(column => row[column])), truncated };
}
const row = (id: string, message = `Hello ${id}`) => ({ id, message, created: "2026-10-25 10:00:00.000Z" });

beforeEach(() => pb.authStore.save(token, user));
afterEach(() => {
  vi.restoreAllMocks();
  pb.authStore.clear();
});

describe("SQL reads", () => {
  it("maps columns to objects and refuses truncated results", async () => {
    vi.spyOn(pb, "send").mockResolvedValueOnce(response([row("greeting0000001")])).mockResolvedValueOnce(response([], true));
    expect(await query("SELECT 1")).toEqual([row("greeting0000001")]);
    await expect(query("SELECT 1")).rejects.toBeInstanceOf(TruncatedQueryError);
  });
  it("lists own greetings newest first through the SQL endpoint", async () => {
    const send = vi.spyOn(pb, "send").mockResolvedValue(response([row("greeting0000002"), row("greeting0000001")]));
    expect((await listGreetings()).map(g => g.id)).toEqual(["greeting0000002", "greeting0000001"]);
    expect(send).toHaveBeenCalledWith("/api/context/query", {
      method: "POST",
      body: { sql: "SELECT id, message, created FROM greetings WHERE owner='user00000000001' ORDER BY created DESC, id DESC LIMIT 100" },
    });
  });
  it("refuses malformed rows and anonymous reads", async () => {
    vi.spyOn(pb, "send").mockResolvedValue(response([{ id: "x' OR 1", message: "m", created: "c" }]));
    await expect(listGreetings()).rejects.toThrow(/Invalid greeting/);
    pb.authStore.clear();
    await expect(listGreetings()).rejects.toThrow(/sign in/);
  });
  it("clears the session when the server rejects its token", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ message: "Unauthorized" }), { status: 401, headers: { "Content-Type": "application/json" } }));
    await expect(query("SELECT 1")).rejects.toMatchObject({ status: 401 });
    expect(String(fetch.mock.calls[0][0])).toMatch(/\/api\/context\/query$/);
    expect(pb.authStore.token).toBe("");
  });
  it("discards a response that arrives after the account changed", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      pb.authStore.save(token, { ...user, id: "user00000000002" });
      return new Response(JSON.stringify(response([row("greeting0000001")])), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    await expect(query("SELECT 1")).rejects.toThrow(/Session changed/);
  });
});

describe("writes", () => {
  it("creates a trimmed greeting through the collection API", async () => {
    const create = vi.fn().mockResolvedValue(row("greeting0000003", "Hi"));
    vi.spyOn(pb, "collection").mockReturnValue({ create } as never);
    expect(await addGreeting("  Hi  ")).toEqual(row("greeting0000003", "Hi"));
    expect(pb.collection).toHaveBeenCalledWith("greetings");
    expect(create).toHaveBeenCalledWith({ message: "Hi" });
  });
  it("validates message length before sending", () => {
    expect(() => validMessage("   ")).toThrow(/Write/);
    expect(() => validMessage("x".repeat(501))).toThrow(/500/);
    expect(validMessage("x".repeat(500))).toHaveLength(500);
  });
  it("subscribes to greeting changes and ignores events after sign-out", async () => {
    let handler: () => void = () => {};
    const unsubscribe = vi.fn().mockResolvedValue(undefined);
    const subscribe = vi.fn(async (_topic: string, callback: () => void) => { handler = callback; return unsubscribe; });
    vi.spyOn(pb, "collection").mockReturnValue({ subscribe } as never);
    const notify = vi.fn();
    const stop = await watchGreetings(notify);
    expect(subscribe).toHaveBeenCalledWith("*", expect.any(Function));
    handler();
    expect(notify).toHaveBeenCalledTimes(1);
    logout();
    handler();
    expect(notify).toHaveBeenCalledTimes(1);
    stop();
    expect(unsubscribe).toHaveBeenCalled();
  });
});

describe("full-text search", () => {
  const searchResult = (hits: { id: string; score: number; excerpt: string }[], hasMore = false) =>
    ({ index: "greetings", generation, hits, hasMore, truncated: hasMore });
  it("posts the index alias without a scope and hydrates hits in ranking order", async () => {
    const send = vi.spyOn(pb, "send").mockImplementation(async path => {
      if (path === "/api/context/search")
        return searchResult([{ id: "greeting0000002", score: -2, excerpt: "hello there" }, { id: "greeting0000001", score: -1, excerpt: "hello" }], true);
      return response([row("greeting0000001"), row("greeting0000002")]);
    });
    const page = await searchGreetings(" hello ");
    expect(send.mock.calls[0]).toEqual(["/api/context/search", { method: "POST", body: { index: "greetings", query: "hello", offset: 0, limit: 20 } }]);
    expect(send.mock.calls[1][1]?.body.sql).toBe(
      "SELECT id, message, created FROM greetings WHERE owner='user00000000001' AND id IN ('greeting0000002','greeting0000001') ORDER BY id LIMIT 20",
    );
    expect(page).toMatchObject({ generation, hasMore: true });
    expect(page.hits.map(hit => [hit.id, hit.excerpt])).toEqual([["greeting0000002", "hello there"], ["greeting0000001", "hello"]]);
  });
  it("pins the generation when paging", async () => {
    const send = vi.spyOn(pb, "send").mockResolvedValue(searchResult([]));
    await searchGreetings("hello", 20, 20, generation);
    expect(send.mock.calls[0][1]?.body).toEqual({ index: "greetings", query: "hello", offset: 20, limit: 20, expectedGeneration: generation });
    await expect(searchGreetings("hello", 20)).rejects.toThrow(/page/);
  });
  it("refuses invalid queries before sending", async () => {
    const send = vi.spyOn(pb, "send");
    await expect(searchGreetings(" ")).rejects.toThrow(/1 to 500/);
    await expect(searchGreetings(Array(17).fill("w").join(" "))).rejects.toThrow(/16 words/);
    expect(send).not.toHaveBeenCalled();
  });
  it.each([
    [{ index: "other" }],
    [{ generation: "short" }],
    [{ scope: "space0000000001" }],
  ])("treats a mismatched response as a changed search (%o)", async patch => {
    vi.spyOn(pb, "send").mockResolvedValue({ ...searchResult([]), ...patch });
    await expect(searchGreetings("hello")).rejects.toBeInstanceOf(SearchChangedError);
  });
  it("refuses out-of-order or duplicate hits", async () => {
    vi.spyOn(pb, "send").mockResolvedValueOnce(searchResult([{ id: "greeting0000001", score: -1, excerpt: "" }, { id: "greeting0000002", score: -2, excerpt: "" }]));
    await expect(searchGreetings("hello")).rejects.toThrow(/ranking/);
    vi.spyOn(pb, "send").mockResolvedValueOnce(searchResult([{ id: "greeting0000001", score: -1, excerpt: "" }, { id: "greeting0000001", score: -1, excerpt: "" }]));
    await expect(searchGreetings("hello")).rejects.toThrow(/hit/);
  });
  it("reports a greeting that vanished between search and hydration as changed", async () => {
    vi.spyOn(pb, "send").mockImplementation(async path =>
      path === "/api/context/search" ? searchResult([{ id: "greeting0000001", score: -1, excerpt: "" }]) : response([]));
    await expect(searchGreetings("hello")).rejects.toBeInstanceOf(SearchChangedError);
  });
  it.each([[409, SearchChangedError], [413, SearchUnavailableError], [503, SearchUnavailableError]])("maps status %i", async (status, kind) => {
    vi.spyOn(pb, "send").mockRejectedValue(Object.assign(new Error("failed"), { status }));
    await expect(searchGreetings("hello")).rejects.toBeInstanceOf(kind);
  });
});

describe("session", () => {
  it("refreshes explicitly and ignores a refresh that completes after sign-out", async () => {
    const send = vi.spyOn(pb, "send").mockImplementation(async () => {
      logout();
      return { token: "renewed", record: user };
    });
    await refreshSession();
    expect(send).toHaveBeenCalledWith("/api/collections/users/auth-refresh", { method: "POST", headers: { Authorization: token } });
    expect(pb.authStore.token).toBe("");
  });
});
