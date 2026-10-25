import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Greeting } from "./api";
import Greetings, { formatDate } from "./Greetings";

const greeting = (id: string, message: string): Greeting => ({ id, message, created: "2026-10-25 10:00:00.000Z" });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function watch() {
  const stop = vi.fn();
  let notify = () => {};
  vi.spyOn(api, "watchGreetings").mockImplementation(async callback => { notify = callback; return stop; });
  return { stop, notify: () => notify() };
}

it("lists greetings newest first as plain text", async () => {
  watch();
  vi.spyOn(api, "listGreetings").mockResolvedValue([greeting("greeting0000002", "<b>Second</b>"), greeting("greeting0000001", "First")]);
  render(<Greetings />);
  const list = await screen.findByRole("list", { name: "Greetings, newest first" });
  const items = within(list).getAllByRole("listitem");
  expect(items.map(item => item.querySelector("p")?.textContent)).toEqual(["<b>Second</b>", "First"]);
  expect(list.querySelector("b")).toBeNull();
});

it("shows an empty state", async () => {
  watch();
  vi.spyOn(api, "listGreetings").mockResolvedValue([]);
  render(<Greetings />);
  expect(await screen.findByText(/No greetings yet/)).toBeInTheDocument();
});

it("adds a greeting, clears the field and reloads the list", async () => {
  watch();
  let stored: Greeting[] = [];
  const list = vi.spyOn(api, "listGreetings").mockImplementation(async () => stored);
  const add = vi.spyOn(api, "addGreeting").mockImplementation(async message => (stored = [greeting("greeting0000001", message)])[0]);
  render(<StrictMode><Greetings /></StrictMode>);
  await screen.findByText(/No greetings yet/);
  const input = screen.getByLabelText("New greeting");
  expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  fireEvent.change(input, { target: { value: "Hello" } });
  expect(screen.getByText("495 characters left")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(add).toHaveBeenCalledWith("Hello"));
  expect(await screen.findByText("Hello")).toBeInTheDocument();
  expect(input).toHaveValue("");
  expect(screen.getByRole("status")).toHaveTextContent("Greeting added.");
  // Strict mode mounts twice; the add reloads once more.
  expect(list).toHaveBeenCalledTimes(3);
});

it("keeps the draft and shows an alert when adding fails", async () => {
  watch();
  vi.spyOn(api, "listGreetings").mockResolvedValue([]);
  vi.spyOn(api, "addGreeting").mockRejectedValue(new Error("Failed to create record."));
  render(<Greetings />);
  await screen.findByText(/No greetings yet/);
  fireEvent.change(screen.getByLabelText("New greeting"), { target: { value: "Hello" } });
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Failed to create record.");
  expect(screen.getByLabelText("New greeting")).toHaveValue("Hello");
});

it("reloads on realtime changes and unsubscribes on unmount", async () => {
  const live = watch();
  const list = vi.spyOn(api, "listGreetings").mockResolvedValueOnce([]).mockResolvedValueOnce([greeting("greeting0000001", "From the CLI")]);
  const view = render(<Greetings />);
  await screen.findByText(/No greetings yet/);
  await waitFor(() => expect(api.watchGreetings).toHaveBeenCalled());
  await act(async () => live.notify());
  expect(await screen.findByText("From the CLI")).toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(2);
  view.unmount();
  expect(live.stop).toHaveBeenCalled();
});

it("formats server dates and keeps unparseable values", () => {
  expect(formatDate("2026-10-25 10:00:00.000Z")).not.toBe("2026-10-25 10:00:00.000Z");
  expect(formatDate("not a date")).toBe("not a date");
});
