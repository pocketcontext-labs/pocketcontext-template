import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "dev@example.localhost" };
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pb.authStore.clear();
});

it("shows sign-in until a users session exists, then the reader, and signs out", async () => {
  vi.spyOn(api, "listGreetings").mockResolvedValue([]);
  vi.spyOn(api, "watchGreetings").mockResolvedValue(() => {});
  const refresh = vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  act(() => pb.authStore.save(token, user));
  expect(await screen.findByRole("heading", { name: "Your greetings" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "Search" })).toBeInTheDocument();
  expect(screen.getByText("dev@example.localhost")).toBeInTheDocument();
  expect(refresh).toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  expect(pb.authStore.token).toBe("");
});

it("does not treat another collection's token as a reader session", () => {
  render(<App />);
  act(() => pb.authStore.save(token, { ...user, collectionName: "_superusers" }));
  expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
});
