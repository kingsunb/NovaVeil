import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { AppShell } from "./AppShell";

vi.mock("./Sidebar", () => ({
  Sidebar: ({ onNavigate }: { onNavigate?: () => void }) => (
    <nav aria-label="测试侧栏">
      <a href="/dashboard" onClick={() => onNavigate?.()}>
        总览
      </a>
    </nav>
  ),
}));
vi.mock("./Topbar", () => ({
  Topbar: ({ onOpenNavigation }: { onOpenNavigation: () => void }) => (
    <header>
      测试顶栏
      <button type="button" onClick={onOpenNavigation}>
        打开主导航
      </button>
    </header>
  ),
}));
vi.mock("./CommandPalette", () => ({ CommandPalette: () => null }));

function Navigation() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  return (
    <>
      <p>{pathname}</p>
      <button onClick={() => navigate("/keys")}>切换页面</button>
      <button onClick={() => navigate("?filter=enabled")}>查询参数</button>
      <button onClick={() => navigate("#details")}>锚点</button>
      <button onClick={() => navigate(pathname)}>相同路径</button>
      <button onClick={() => navigate(-1)}>后退</button>
      <button onClick={() => navigate(1)}>前进</button>
    </>
  );
}

function shell() {
  return (
    <MemoryRouter initialEntries={["/groups"]}>
      <AppShell><Navigation /></AppShell>
    </MemoryRouter>
  );
}

describe("AppShell 主区滚动复位", () => {
  it("pathname 切换及前进后退复位同一 main，不操作侧栏或 window", async () => {
    const user = userEvent.setup();
    const windowScroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
    try {
      render(shell());
      const main = screen.getByRole("main");
      const sidebar = screen.getByRole("navigation", { hidden: true });
      sidebar.scrollTop = 80;
      main.scrollTop = 700;
      await user.click(screen.getByRole("button", { name: "切换页面" }));
      expect(screen.getByText("/keys")).toBeInTheDocument();
      expect(screen.getByRole("main")).toBe(main);
      expect(main.scrollTop).toBe(0);

      main.scrollTop = 400;
      await user.click(screen.getByRole("button", { name: "后退" }));
      expect(screen.getByText("/groups")).toBeInTheDocument();
      expect(main.scrollTop).toBe(0);
      main.scrollTop = 300;
      await user.click(screen.getByRole("button", { name: "前进" }));
      expect(screen.getByText("/keys")).toBeInTheDocument();
      expect(main.scrollTop).toBe(0);
      expect(sidebar.scrollTop).toBe(80);
      expect(windowScroll).not.toHaveBeenCalled();
    } finally {
      windowScroll.mockRestore();
    }
  });

  it("查询参数、hash、同路径导航及普通重渲染不复位", async () => {
    const user = userEvent.setup();
    const view = render(shell());
    const main = screen.getByRole("main");
    main.scrollTop = 500;
    for (const name of ["查询参数", "锚点", "相同路径"]) {
      await user.click(screen.getByRole("button", { name }));
      expect(main.scrollTop).toBe(500);
    }
    view.rerender(shell());
    expect(main.scrollTop).toBe(500);
    // 命令面板状态更新仍不触发路由滚动副作用。
    await user.keyboard("{Control>}k{/Control}");
    expect(main.scrollTop).toBe(500);
  });
});

describe("AppShell 移动抽屉焦点", () => {
  it("圈定 Tab，关闭后焦点回到打开按钮", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("max-width"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
      onchange: null,
    }));
    try {
      render(shell());
      const openBtn = screen.getByRole("button", { name: "打开主导航" });
      openBtn.focus();
      await user.click(openBtn);
      const link = screen.getByRole("link", { name: "总览" });
      await waitFor(() => expect(link).toHaveFocus());
      await user.tab();
      expect(link).toHaveFocus();
      await user.keyboard("{Escape}");
      await waitFor(() => expect(openBtn).toHaveFocus());
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
