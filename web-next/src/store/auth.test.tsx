import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AuthProvider, useAuth } from "./auth";

function makeQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

function wrapper(qc?: QueryClient) {
  const client = qc ?? makeQueryClient();
  return {
    client,
    Wrapper: function Wrapper({ children }: { children: React.ReactNode }) {
      return (
        <QueryClientProvider client={client}>
          <AuthProvider>{children}</AuthProvider>
        </QueryClientProvider>
      );
    },
  };
}

beforeEach(() => {
  localStorage.clear();
});

describe("useAuth", () => {
  it("启动时调 /user/status 探活，401 后保持未登录", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 401, message: "unauthorized", data: null }),
            { status: 401, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(false);
    });
    vi.unstubAllGlobals();
  });

  it("启动时 /user/status 200 → 标记已登录", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              code: 200,
              message: "success",
              data: { must_change_password: true },
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });
    expect(result.current.mustChangePassword).toBe(true);
    vi.unstubAllGlobals();
  });

  it("探活 200 携带 username → 回填显示名（刷新后 Topbar 不再是假 admin）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              code: 200,
              message: "success",
              data: { username: "ops", must_change_password: false },
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });
    expect(result.current.username).toBe("ops");
    vi.unstubAllGlobals();
  });

  it("探活 200 缺 username（旧后端）→ 保持 null，Topbar 走兜底而非空串", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              code: 200,
              message: "success",
              data: { must_change_password: false },
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });
    expect(result.current.username).toBeNull();
    vi.unstubAllGlobals();
  });

  it("login() 成功后状态置已登录", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/user/login")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { must_change_password: false },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: { must_change_password: false } }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.login("admin", "secret");
    });
    expect(result.current.isAuthenticated).toBe(true);
    expect(result.current.username).toBe("admin");
    vi.unstubAllGlobals();
  });

  it("logout() 清空状态，即使后端 500", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 500, message: "boom", data: null }),
            { status: 500, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.logout();
    });
    expect(result.current.isAuthenticated).toBe(false);
    vi.unstubAllGlobals();
  });

  it("logout() 清空全部 React Query 缓存（明文 Key 等不复用）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/user/logout")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({ code: 200, message: "success", data: null }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 401, message: "unauthorized", data: null }),
            { status: 401, headers: { "content-type": "application/json" } },
          ),
        );
      }),
    );
    const qc = makeQueryClient();
    // 预置敏感缓存：明文 API Key 与渠道明文 Key。
    qc.setQueryData(["apikeys", "secret", 42], "sk-PLAINTEXT-LEAK");
    qc.setQueryData(["channels", 1, "keys"], [
      { id: "k1", key: "sk-CHANNEL-PLAIN", remark: "" },
    ]);
    expect(qc.getQueryData(["apikeys", "secret", 42])).toBe(
      "sk-PLAINTEXT-LEAK",
    );

    const { Wrapper } = wrapper(qc);
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.logout();
    });

    // 登出后缓存被彻底清除，敏感明文不复用。
    expect(qc.getQueryData(["apikeys", "secret", 42])).toBeUndefined();
    expect(qc.getQueryData(["channels", 1, "keys"])).toBeUndefined();
    vi.unstubAllGlobals();
  });

  it("refreshStatus() 后端 401 → 退回未登录", async () => {
    // 首屏 /user/status 用本地缓存假数据, 让 hook 启动时进入已登录态。
    const calls: Array<{ url: string; status: number }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, _init?: RequestInit) => {
        if (url.includes("/user/status")) {
          const status = calls.filter((c) => c.url.includes("/user/status")).length === 0 ? 200 : 401;
          calls.push({ url, status });
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: status,
                message: status === 200 ? "ok" : "unauthorized",
                data: { must_change_password: false },
              }),
              { status, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: null }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    // 首次 status: 200 → 标记已登录。
    await waitFor(() => expect(result.current.isAuthenticated).toBe(true));
    // 触发一次手动刷新, 后端返 401。
    await act(async () => {
      await result.current.refreshStatus();
    });
    await waitFor(() => expect(result.current.isAuthenticated).toBe(false));
    vi.unstubAllGlobals();
  });

  it("login() 后启动探活 401 返回时不覆盖已登录态（generation 守卫）", async () => {
    // 模拟竞态：启动探活挂起 → login 成功 → 探活才返回 401。
    let resolveStatus: ((r: Response) => void) | null = null;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/user/status")) {
          return new Promise<Response>((resolve) => {
            resolveStatus = resolve;
          });
        }
        if (url.includes("/user/login")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { must_change_password: false },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: null }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }),
    );
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper });
    // login 成功（generation 递增到 1）。
    await act(async () => {
      await result.current.login("admin", "secret");
    });
    expect(result.current.isAuthenticated).toBe(true);
    // 探活 401 迟到返回：generation 已过期，不应覆盖登录态。
    await act(async () => {
      resolveStatus?.(
        new Response(
          JSON.stringify({ code: 401, message: "unauthorized", data: null }),
          { status: 401, headers: { "content-type": "application/json" } },
        ),
      );
    });
    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });
    vi.unstubAllGlobals();
  });
});
