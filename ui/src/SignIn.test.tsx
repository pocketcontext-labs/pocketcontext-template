import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import SignIn from "./SignIn";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("signs in with email and password", async () => {
  const login = vi.spyOn(api, "loginPassword").mockResolvedValue();
  render(<SignIn />);
  expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  expect(screen.getByText("TemplateContext")).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "dev@example.localhost" } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "synthetic-password" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await waitFor(() => expect(login).toHaveBeenCalledWith("dev@example.localhost", "synthetic-password"));
  expect(screen.getByText(/templatecontext open/)).toBeInTheDocument();
});

it("shows a failed sign-in as an alert and re-enables the form", async () => {
  vi.spyOn(api, "loginPassword").mockRejectedValue(new Error("Failed to authenticate."));
  render(<SignIn />);
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "dev@example.localhost" } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "wrong" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Failed to authenticate.");
  expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
});

it("switches to a one-time code and redeems it", async () => {
  const cancel = vi.spyOn(api, "cancelLogin");
  const code = vi.spyOn(api, "loginCode").mockResolvedValue();
  render(<SignIn />);
  fireEvent.click(screen.getByRole("button", { name: "Use a sign-in code instead" }));
  expect(cancel).toHaveBeenCalled();
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(screen.getByText(/login-code/)).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Sign-in code"), { target: { value: "0123456789abcdef" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await waitFor(() => expect(code).toHaveBeenCalledWith("0123456789abcdef"));
  fireEvent.click(screen.getByRole("button", { name: "Use a password instead" }));
  expect(screen.getByLabelText("Password")).toBeInTheDocument();
});

it("cancels a pending sign-in when it unmounts", () => {
  const cancel = vi.spyOn(api, "cancelLogin");
  render(<SignIn />).unmount();
  expect(cancel).toHaveBeenCalled();
});
