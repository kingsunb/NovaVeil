import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { Sidebar } from "./Sidebar";
import {
  loadChannelsPage,
  loadDashboardPage,
  loadLogsPage,
} from "@/lib/route-loaders";

vi.mock("@/lib/route-loaders", () => ({
  loadLoginPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadDashboardPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadChannelsPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadCustomModelsPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadGroupsPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadModelEvalPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadMaskPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadKeysPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadLogsPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadSettingsPage: vi.fn(() => Promise.resolve({ default: () => null })),
  loadChatPage: vi.fn(() => Promise.resolve({ default: () => null })),
}));

const KEY = "nv-sidebar-collapsed";

function renderSidebar() {
  return render(
    <MemoryRouter
      initialEntries={["/dashboard"]}
    >
      <Sidebar />
    </MemoryRouter>,
  );
}

describe("<Sidebar />", () => {
  beforeEach(() => {
    localStorage.removeItem(KEY);
  });

  it("默认展开，宽度为 w-60", () => {
    renderSidebar();
    const sb = screen.getByTestId("sidebar");
    expect(sb.dataset.collapsed).toBe("false");
    expect(sb.className).toMatch(/w-60/);
  });

  it("点击折叠按钮后变窄且 data-collapsed=true", async () => {
    const user = userEvent.setup();
    renderSidebar();
    await user.click(screen.getByTestId("collapse-btn"));
    const sb = screen.getByTestId("sidebar");
    expect(sb.dataset.collapsed).toBe("true");
    expect(sb.className).toMatch(/w-14/);
    expect(localStorage.getItem(KEY)).toBe("1");
  });

  it("再次点击恢复", async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, "1");
    renderSidebar();
    await user.click(screen.getByTestId("collapse-btn"));
    const sb = screen.getByTestId("sidebar");
    expect(sb.dataset.collapsed).toBe("false");
    expect(localStorage.getItem(KEY)).toBe("0");
  });

  it("折叠态下 label 文本被隐藏（accessibility 通过 title 提示）", () => {
    localStorage.setItem(KEY, "1");
    renderSidebar();
    // NavLink 折叠后只展示图标，文字节点被 hidden class 隐藏
    const link = screen.getByTitle("总览");
    expect(link).toBeInTheDocument();
  });

  it("aria-label 在折叠态为「展开侧栏」", () => {
    localStorage.setItem(KEY, "1");
    renderSidebar();
    expect(screen.getByLabelText("展开侧栏")).toBeInTheDocument();
  });
});

describe("<Sidebar /> 路由 chunk 预加载 (§3.3)", () => {
  beforeEach(() => {
    localStorage.removeItem(KEY);
    vi.clearAllMocks();
  });

  it("hover 导航项触发对应 loader 预加载", () => {
    renderSidebar();
    fireEvent.mouseEnter(screen.getByLabelText("总览"));
    expect(vi.mocked(loadDashboardPage)).toHaveBeenCalled();
    fireEvent.mouseEnter(screen.getByLabelText("渠道"));
    expect(vi.mocked(loadChannelsPage)).toHaveBeenCalled();
  });

  it("focus 导航项同样触发预加载（键盘可达）", () => {
    renderSidebar();
    fireEvent.focus(screen.getByLabelText("日志"));
    expect(vi.mocked(loadLogsPage)).toHaveBeenCalled();
  });

  it("未 hover 的路由 loader 不被调用", () => {
    renderSidebar();
    fireEvent.mouseEnter(screen.getByLabelText("总览"));
    expect(vi.mocked(loadLogsPage)).not.toHaveBeenCalled();
  });
});
