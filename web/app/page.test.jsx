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

test("streams pending submission updates after login", async () => {
  const encoded = new TextEncoder().encode(
    'event: submission\ndata: {"submission_id":"s1","language_id":"python","status":"FINAL","verdict":"ACCEPTED","runtime_ms":8}\n\n',
  );
  let read = false;
  global.fetch = vi.fn().mockImplementation(async (url) => {
    if (url.endsWith("/submissions/s1/events")) {
      return {
        ok: true,
        body: {
          getReader: () => ({
            read: async () => {
              if (read) return { done: true };
              read = true;
              return { done: false, value: encoded };
            },
          }),
        },
      };
    }
    return {
      ok: true,
      json: async () =>
        url.endsWith("/login")
          ? { token: "session" }
          : url.endsWith("/users/me")
            ? { username: "alice", role: "user" }
            : url.endsWith("/users/me/submissions")
              ? {
                  submissions: [
                    {
                      submission_id: "s1",
                      language_id: "python",
                      status: "QUEUED",
                      runtime_ms: 0,
                    },
                  ],
                }
              : { problems: [] },
    };
  });
  render(<Page />);
  fireEvent.change(screen.getByLabelText("Email"), {
    target: { value: "alice@example.com" },
  });
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: "password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Log in" }));
  await waitFor(() => expect(screen.getByText("ACCEPTED")).toBeTruthy());
  expect(global.fetch).toHaveBeenCalledWith(
    "/v1/submissions/s1/events",
    expect.objectContaining({
      headers: expect.objectContaining({ Authorization: "Bearer session" }),
    }),
  );
});
