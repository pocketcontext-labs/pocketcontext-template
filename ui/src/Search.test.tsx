import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { api, SearchChangedError, type SearchPage } from "./api";
import Search from "./Search";

const generation = "b".repeat(64);
const hit = (id: string, message: string, excerpt = message) => ({ id, message, excerpt, score: -1, created: "2026-10-25 10:00:00.000Z" });
const page = (hits: ReturnType<typeof hit>[], hasMore = false): SearchPage => ({ generation, hits, hasMore });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function search(term: string) {
  fireEvent.change(screen.getByRole("searchbox", { name: "Search greetings" }), { target: { value: term } });
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
}

it("searches, shows plain-text results and pages within one generation", async () => {
  const send = vi.spyOn(api, "searchGreetings")
    .mockResolvedValueOnce(page([hit("greeting0000001", "Hello world", "Hello <i>world</i>")], true))
    .mockResolvedValueOnce(page([hit("greeting0000002", "Hello again")]));
  // Strict mode re-runs effects as development builds and hot reload do.
  render(<StrictMode><Search /></StrictMode>);
  expect(screen.getByRole("button", { name: "Search" })).toBeDisabled();
  search("hello");
  const results = await screen.findByRole("list", { name: "Search results" });
  expect(within(results).getByText("Hello <i>world</i>")).toBeInTheDocument();
  expect(screen.getByText(/1\+ match for/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "More results" }));
  await screen.findByText("Hello again");
  expect(send.mock.calls).toEqual([["hello", 0, 20, undefined], ["hello", 1, 20, generation]]);
  expect(screen.queryByRole("button", { name: "More results" })).toBeNull();
  expect(screen.getByText(/2 matches for/)).toBeInTheDocument();
});

it("reports no matches", async () => {
  vi.spyOn(api, "searchGreetings").mockResolvedValue(page([]));
  render(<Search />);
  search("nothing");
  expect(await screen.findByText(/No greetings match “nothing”/)).toBeInTheDocument();
});

it("shows search errors and drops stale results", async () => {
  vi.spyOn(api, "searchGreetings").mockResolvedValueOnce(page([hit("greeting0000001", "Hello")])).mockRejectedValueOnce(new SearchChangedError());
  render(<Search />);
  search("hello");
  await screen.findByText("Hello");
  search("hello");
  expect(await screen.findByRole("alert")).toHaveTextContent(/changed/);
  expect(screen.queryByText("Hello")).toBeNull();
});

it("validates the query locally and clears results", async () => {
  const send = vi.spyOn(api, "searchGreetings").mockResolvedValue(page([hit("greeting0000001", "Hello")]));
  render(<Search />);
  search(Array(17).fill("word").join(" "));
  expect(await screen.findByRole("alert")).toHaveTextContent(/16 words/);
  expect(send).not.toHaveBeenCalled();
  search("hello");
  await screen.findByText("Hello");
  fireEvent.click(screen.getByRole("button", { name: "Clear" }));
  await waitFor(() => expect(screen.queryByText("Hello")).toBeNull());
  expect(screen.getByRole("searchbox")).toHaveValue("");
});
