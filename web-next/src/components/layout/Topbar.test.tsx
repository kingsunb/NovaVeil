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

function renderTopbar(initialEntry = "/channels") {
  const onOpenNavigation = vi.fn();
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Topbar onOpenNavigation={onOpenNavigation} />
    </MemoryRouter>,
  );
  return { onOpenNavigation };
}

beforeEach(() => vi.clearAllMocks());

describe("Topbar 模型评估标题", () => {
  it.each(["/model-eval", "/model-eval?view=history"])("%s 在公共顶栏显示标题与说明", (initialEntry) => {
    renderTopbar(initialEntry);
    expect(screen.getByRole("heading", { name: "模型评估" })).toBeInTheDocument();
    expect(screen.getByText("用同一道 SVG 动画题比较模型表现")).toBeInTheDocument();
  });
});

describe("Topbar 操作区", () => {
  it("不再渲染命令面板入口与快捷键提示", () => {
    renderTopbar();
    expect(screen.queryByRole("button", { name: "打开命令面板" })).toBeNull();
    expect(screen.queryByText("Ctrl/Cmd + K")).toBeNull();
    expect(screen.getByRole("heading", { name: "渠道" })).toBeInTheDocument();
  });

  it("保留导航、主题和退出登录操作", async () => {
    const user = userEvent.setup();
    const { onOpenNavigation } = renderTopbar();
    await user.click(screen.getByRole("button", { name: "打开主导航" }));
    await user.click(screen.getByRole("button", { name: "切换主题" }));
    await user.click(screen.getByTitle("退出登录"));
    expect(onOpenNavigation).toHaveBeenCalledOnce();
    expect(toggle).toHaveBeenCalledOnce();
    expect(logout).toHaveBeenCalledOnce();
  });
});
