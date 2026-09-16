import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { Topbar } from "./Topbar";

const { toggle, logout } = vi.hoisted(() => ({ toggle: vi.fn(), logout: vi.fn() }));
vi.mock("./ThemeProvider", () => ({
  useTheme: () => ({ resolved: "light", toggle }),
}));
vi.mock("@/store/auth", () => ({
  useAuth: () => ({ username: "admin", logout }),
}));

function renderTopbar() {
  const onOpenCommand = vi.fn();
  const onOpenNavigation = vi.fn();
  render(
    <MemoryRouter initialEntries={["/channels"]}>
      <Topbar onOpenCommand={onOpenCommand} onOpenNavigation={onOpenNavigation} />
    </MemoryRouter>,
  );
  return { onOpenCommand, onOpenNavigation };
}

beforeEach(() => vi.clearAllMocks());

describe("Topbar command entry", () => {
  it("exposes an accessible button and shortcut with compact mobile styling", () => {
    renderTopbar();
    const button = screen.getByRole("button", { name: "打开命令面板" });
    expect(button).toHaveAttribute("type", "button");
    expect(button).toHaveAttribute("aria-haspopup", "dialog");
    expect(button).toHaveAttribute("aria-keyshortcuts", "Control+k Meta+k");
    expect(button).toHaveAttribute("title", "打开命令面板（Ctrl/Cmd + K）");
    expect(button).toHaveClass("h-11", "w-11", "shrink-0");
    expect(screen.getByText("Ctrl/Cmd + K")).toHaveClass("hidden", "lg:inline");
    expect(screen.getByRole("heading", { name: "渠道" })).toBeInTheDocument();
  });

  it("opens by mouse and touch without invoking other controls", async () => {
    const user = userEvent.setup();
    const { onOpenCommand, onOpenNavigation } = renderTopbar();
    const button = screen.getByRole("button", { name: "打开命令面板" });
    await user.click(button);
    await user.pointer([
      { keys: "[TouchA>]", target: button },
      { keys: "[/TouchA]", target: button },
    ]);
    expect(onOpenCommand).toHaveBeenCalledTimes(2);
    expect(onOpenNavigation).not.toHaveBeenCalled();
    expect(toggle).not.toHaveBeenCalled();
    expect(logout).not.toHaveBeenCalled();
  });

  it("is reachable by Tab and activates with Enter and Space", async () => {
    const user = userEvent.setup();
    const { onOpenCommand } = renderTopbar();
    await user.tab(); // Mobile navigation precedes command entry.
    await user.tab();
    expect(screen.getByRole("button", { name: "打开命令面板" })).toHaveFocus();
    await user.keyboard("{Enter}");
    await user.keyboard(" ");
    expect(onOpenCommand).toHaveBeenCalledTimes(2);
  });

  it("preserves navigation, theme and logout actions", async () => {
    const user = userEvent.setup();
    const { onOpenCommand, onOpenNavigation } = renderTopbar();
    await user.click(screen.getByRole("button", { name: "打开主导航" }));
    await user.click(screen.getByRole("button", { name: "切换主题" }));
    await user.click(screen.getByTitle("退出登录"));
    expect(onOpenNavigation).toHaveBeenCalledOnce();
    expect(toggle).toHaveBeenCalledOnce();
    expect(logout).toHaveBeenCalledOnce();
    expect(onOpenCommand).not.toHaveBeenCalled();
  });
});
