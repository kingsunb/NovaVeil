import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Probe() {
  const { theme, resolved, setTheme } = useTheme();
  return (
    <div>
      <span data-testid="resolved">{resolved}</span>
      <span data-testid="theme">{theme}</span>
      <button onClick={() => setTheme("dark")}>choose-dark</button>
      <button onClick={() => setTheme("system")}>choose-system</button>
    </div>
  );
}

function installMatchMedia() {
  const listeners = new Set<(e: { matches: boolean }) => void>();
  const matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn((_event: string, cb: (e: { matches: boolean }) => void) => {
      listeners.add(cb);
    }),
    removeEventListener: vi.fn((_event: string, cb: (e: { matches: boolean }) => void) => {
      listeners.delete(cb);
    }),
    dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal("matchMedia", matchMedia);
  return listeners;
}

describe("<ThemeProvider /> 系统主题变化", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("system 模式下 matchMedia 变化同步更新 context.resolved，而不再停留旧值", async () => {
    const listeners = installMatchMedia();

    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByTestId("resolved")).toHaveTextContent("light");

    // 系统切到深色：以前只改 DOM dataset，context.resolved 不更新（审计 FE-05）。
    act(() => {
      for (const cb of listeners) cb({ matches: true });
    });
    await screen.findByText("dark");
    expect(screen.getByTestId("resolved")).toHaveTextContent("dark");

    // 系统切回浅色，同样同步。
    act(() => {
      for (const cb of listeners) cb({ matches: false });
    });
    await screen.findByText("light");

    vi.unstubAllGlobals();
  });

  it("显式主题下不跟随系统变化", async () => {
    const user = userEvent.setup();
    const listeners = installMatchMedia();

    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    await user.click(screen.getByRole("button", { name: "choose-dark" }));
    await waitFor(() =>
      expect(screen.getByTestId("resolved")).toHaveTextContent("dark"),
    );

    act(() => {
      for (const cb of listeners) cb({ matches: true });
    });
    expect(screen.getByTestId("resolved")).toHaveTextContent("dark");
    expect(screen.getByTestId("theme")).toHaveTextContent("dark");

    vi.unstubAllGlobals();
  });
});
