import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import Brand from "./Brand";

afterEach(cleanup);
it("shows the decorative mark and the product name", () => {
  const { container } = render(<Brand />);
  expect(screen.getByText("TemplateContext")).toBeInTheDocument();
  const image = container.querySelector("img")!;
  expect(image).toHaveAttribute("alt", "");
  expect(image.getAttribute("src")).toMatch(/logo\.svg/);
});
