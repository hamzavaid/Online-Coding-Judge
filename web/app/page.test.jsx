import React from "react";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { afterEach, test, expect, vi } from "vitest";
import Page from "./page";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("loads public problems and displays statements as text", async () => {
  global.fetch = vi
    .fn()
    .mockResolvedValue({
      ok: true,
      json: async () => ({
        problems: [
          {
            id: "p1",
            title: "Sum",
            statement: "<script>bad</script>",
            languages: ["python", "cpp"],
          },
        ],
      }),
    });
  render(<Page />);
  fireEvent.click(await screen.findByText("Sum"));
  expect(await screen.findByText("<script>bad</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBeNull();
});

test("login sends credentials and exposes submission controls", async () => {
  global.fetch = vi
    .fn()
    .mockImplementation(async (url) => ({
      ok: true,
      json: async () =>
        url.endsWith("/login")
          ? { token: "session" }
          : url.endsWith("/me")
            ? { username: "alice", role: "user" }
            : { problems: [], submissions: [] },
    }));
  render(<Page />);
  fireEvent.change(screen.getByLabelText("Email"), {
    target: { value: "alice@example.com" },
  });
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: "password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Log in" }));
  await waitFor(() =>
    expect(screen.getByText("Signed in as alice")).toBeTruthy(),
  );
  expect(global.fetch).toHaveBeenCalledWith(
    "/v1/auth/login",
    expect.objectContaining({ method: "POST" }),
  );
});
