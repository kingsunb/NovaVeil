import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import App from "./App";
import {
  CardGridSkeleton, LoginSkeleton, LogsSkeleton, PageSkeleton,
  SettingsSkeleton, TableSkeleton,
} from "@/components/ui/skeleton";

const state = vi.hoisted(() => ({
  page: vi.fn(),
  auth: {
    isAuthenticated: true, username: "admin", mustChangePassword: false,
    isBootstrapping: false, logout: vi.fn(), refreshStatus: vi.fn(),
  },
}));
vi.mock("@/store/auth", () => ({ useAuth: () => state.auth }));
vi.mock("@/lib/flags", () => ({
  loadFlags: () => new Promise(() => {}), shouldUseNewWeb: () => true,
}));
vi.mock("@/lib/version-check", () => ({ useBuildVersionCheck: vi.fn() }));
vi.mock("@/components/layout/AppShell", () => ({
  AppShell: ({ children }: { children: ReactNode }) => <main>{children}</main>,
}));
vi.mock("@/components/auth/ForceChangePassword", () => ({
  ForceChangePassword: () => <div>强制改密门</div>,
}));
// 页面模块独立 mock，避免加载业务依赖；抛出未决 Promise 模拟 chunk 等待。
vi.mock("@/pages/Login", () => ({ default: state.page }));
vi.mock("@/pages/Dashboard", () => ({ default: state.page }));
vi.mock("@/pages/Channels", () => ({ default: state.page }));
vi.mock("@/pages/CustomModels", () => ({ default: state.page }));
vi.mock("@/pages/Groups", () => ({ default: state.page }));
vi.mock("@/pages/ModelEval", () => ({ default: state.page }));
vi.mock("@/pages/Mask", () => ({ default: state.page }));
vi.mock("@/pages/Keys", () => ({ default: state.page }));
vi.mock("@/pages/Logs", () => ({ default: state.page }));
vi.mock("@/pages/Settings", () => ({ default: state.page }));
vi.mock("@/pages/Chat", () => ({ default: state.page }));

beforeEach(() => {
  state.page.mockClear();
  state.auth.isAuthenticated = true;
  state.auth.isBootstrapping = false;
  state.auth.mustChangePassword = false;
  const pending = new Promise(() => {});
  state.page.mockImplementation(() => { throw pending; });
});

function appAt(path: string) {
  return <MemoryRouter initialEntries={[path]}><App /></MemoryRouter>;
}

describe("路由 pending 分类", () => {
  it.each([
    ["/dashboard", PageSkeleton],
    ["/channels", TableSkeleton],
    ["/custom-models", TableSkeleton],
    ["/model-eval", TableSkeleton],
    ["/mask", TableSkeleton],
    ["/keys", TableSkeleton],
    ["/groups", CardGridSkeleton],
    ["/logs", LogsSkeleton],
    ["/settings", SettingsSkeleton],
    ["/chat", PageSkeleton],
    ["/login", LoginSkeleton],
  ] as const)("%s 复用对应骨架，内容就绪后移除占位", async (path, Skeleton) => {
    const expected = render(<Skeleton />).container.innerHTML;
    cleanup();
    state.auth.isAuthenticated = path !== "/login";
    const view = render(appAt(path));
    await screen.findAllByRole("status", { name: "加载中" });
    const content = path === "/login" ? view.container : screen.getByRole("main");
    expect(content.innerHTML).toBe(expected);

    state.page.mockImplementation(() => <div>目标页面已就绪</div>);
    view.rerender(appAt(path));
    expect(await screen.findByText("目标页面已就绪")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("启动探活和强制改密仍优先于业务路由", () => {
    state.auth.isBootstrapping = true;
    const view = render(appAt("/keys"));
    expect(screen.queryByRole("main")).not.toBeInTheDocument();
    expect(state.page).not.toHaveBeenCalled();

    state.auth.isBootstrapping = false;
    state.auth.mustChangePassword = true;
    view.rerender(appAt("/keys"));
    expect(screen.getByText("强制改密门")).toBeInTheDocument();
    expect(screen.queryByRole("main")).not.toBeInTheDocument();
    expect(state.page).not.toHaveBeenCalled();
  });

  it("页面错误仍由现有边界显示，重试可恢复而非停留在骨架", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      state.page.mockImplementation(() => { throw new Error("route render failed"); });
      render(appAt("/keys"));
      expect(await screen.findByRole("alert")).toHaveTextContent("这块内容出错了");
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
      state.page.mockImplementation(() => <div>目标页面已就绪</div>);
      await userEvent.setup().click(screen.getByRole("button", { name: "重试" }));
      expect(await screen.findByText("目标页面已就绪")).toBeInTheDocument();
    } finally {
      consoleError.mockRestore();
    }
  });
});
