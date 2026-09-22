import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "@/store/auth";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import LoginPage from "./Login";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <MemoryRouter>{children}</MemoryRouter>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );
}

function loginBody(fetchMock: ReturnType<typeof vi.fn>) {
  const call = fetchMock.mock.calls.find(([url]) =>
    String(url).includes("/user/login"),
  );
  return JSON.parse((call?.[1] as RequestInit)?.body as string);
}

describe("<LoginPage />", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn((url: string) => {
      if (url.includes("/user/status")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 401, message: "unauthorized", data: null }),
            { status: 401, headers: { "content-type": "application/json" } },
          ),
        );
      }
      if (url.includes("/user/login")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              code: 200,
              message: "success",
              data: { username: "admin", must_change_password: false },
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
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => vi.unstubAllGlobals());

  it("默认勾选「信任此设备」，提交时透传 remember=true", async () => {
    render(<LoginPage />, { wrapper: Wrapper });
    const checkbox = screen.getByRole("checkbox", {
      name: "信任此设备（24 小时内免登录）",
    });
    expect(checkbox).toBeChecked();

    fireEvent.change(screen.getByLabelText("用户名"), {
      target: { value: "admin" },
    });
    fireEvent.change(screen.getByLabelText("密码"), {
      target: { value: "secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() => {
      expect(loginBody(fetchMock).remember).toBe(true);
    });
  });

  it("取消勾选后提交，remember=false（单次会话）", async () => {
    render(<LoginPage />, { wrapper: Wrapper });
    fireEvent.click(
      screen.getByRole("checkbox", {
        name: "信任此设备（24 小时内免登录）",
      }),
    );
    fireEvent.change(screen.getByLabelText("用户名"), {
      target: { value: "admin" },
    });
    fireEvent.change(screen.getByLabelText("密码"), {
      target: { value: "secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() => {
      expect(loginBody(fetchMock).remember).toBe(false);
    });
  });

  it("不再展示「首次登录请使用控制台初始化提示的管理员凭据」提示", async () => {
    render(<LoginPage />, { wrapper: Wrapper });
    // 等 AuthProvider 启动探活（/user/status → 401）落定，避免异步状态更新泄漏 act 警告。
    await waitFor(() => {
      expect(screen.queryByText(/首次登录请使用控制台初始化提示/)).toBeNull();
    });
  });
});