import { expect, it } from "vitest";
import { message } from "./errors";

it("prefers the server's message, then the error's, then a generic one", () => {
  expect(message(Object.assign(new Error("Generic"), { response: { message: "From server" } }))).toBe("From server");
  expect(message(new Error("Local"))).toBe("Local");
  expect(message("text")).toMatch(/Something went wrong/);
});
