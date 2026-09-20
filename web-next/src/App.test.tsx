import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import App from "./App";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import { AuthProvider } from "@/store/auth";
import { clearFlagsCache } from "@/lib/flags";

const STORAGE_KEY = "nv-auth";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <MemoryRouter>
            {children}
          </MemoryRouter>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );
}

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function plainJson(data: unknown) {
  // /__flags/*.json 是纯 JSON，不带信封
  return new Response(JSON.stringify(data), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

beforeEach(() => {
  clearFlagsCache();
  localStorage.clear();
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

/**
 * 模拟不同 flags.json 返回来覆盖默认
 */
function mockFlags(runtime: object) {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url.includes("/__flags/default.json")) {
        return Promise.resolve(plainJson({}));
      }
      if (url.includes("/__flags/runtime.json")) {
        return Promise.resolve(plainJson(runtime));
      }
      if (url.includes("/user/status")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: { must_change_password: false } }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }
      if (url.includes("/log/errors")) {
        return Promise.resolve(jsonOk([]));
      }
      return Promise.resolve(jsonOk(null));
    }),
  );
}

describe("<App /> P3 灰度路由", () => {
  it("new-web=false → 渲染回退提示页（含 legacy 链接）", async () => {
    mockFlags({ "new-web": false, "rollout-percent": 0 });
    render(<App />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("您当前使用经典版控制台")).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /进入旧版控制台/ })).toBeInTheDocument();
  });

  it("ab-mode=old → 强制走旧（即使 new-web=true）", async () => {
    mockFlags({ "new-web": true, "rollout-percent": 100, "ab-mode": "old" });
    render(<App />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("您当前使用经典版控制台")).toBeInTheDocument();
    });
  });

  it("new-web=true + 100% → 正常进入新前端（Dashboard 加载）", async () => {
    mockFlags({ "new-web": true, "rollout-percent": 100 });
    render(<App />, { wrapper: Wrapper });
    // Dashboard 是懒加载 + flags 异步拉取, 用 findByText 等待最终渲染稳定,
    // 避免 waitFor + getByText 在 CI 上因时序竞态间歇性失败。
    expect(await screen.findByText("总请求", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});

describe("<App /> 首登强制改密", () => {
  let statusMustChange: boolean;
  function mockMustChangeFlags() {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/__flags/")) {
          return Promise.resolve(plainJson({}));
        }
        if (url.includes("/user/status")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { username: "admin", must_change_password: statusMustChange },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        if (url.includes("/user/change-password")) {
          // 与真实后端一致：改密成功后 must_change_password 即刻清除
          statusMustChange = false;
          return Promise.resolve(jsonOk(null));
        }
        if (url.includes("/log/errors")) {
          return Promise.resolve(jsonOk([]));
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  beforeEach(() => {
    statusMustChange = true;
  });

  it("mustChangePassword=true → 只渲染改密页，控制台不可达", async () => {
    mockMustChangeFlags();
    render(<App />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("设置新密码")).toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText("当前密码")).toBeInTheDocument();
    // 控制台壳层与业务页都不出现
    expect(screen.queryByText("总请求")).not.toBeInTheDocument();
  });

  it("改密成功后自动进入控制台，无需刷新", async () => {
    mockMustChangeFlags();
    const user = userEvent.setup();
    render(<App />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("设置新密码")).toBeInTheDocument();
    });
    await user.type(screen.getByPlaceholderText("当前密码"), "old-password");
    await user.type(screen.getByPlaceholderText("新密码（≥8 位）"), "new-password-123");
    await user.type(screen.getByPlaceholderText("确认新密码"), "new-password-123");
    await user.click(screen.getByRole("button", { name: "更新密码" }));
    // ChangePasswordForm 成功后 refreshStatus() 拉回 must_change_password=false，
    // 改密门自动放行进入 Dashboard
    expect(await screen.findByText("总请求", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});
