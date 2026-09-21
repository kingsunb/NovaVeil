import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import DashboardPage from "./Dashboard";

// 把 useQuery 用的 queryClient 单独创建，避免测试间缓存污染
function makeWrapper() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        {children}
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe("<DashboardPage /> KPI tone", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        // 默认返空数据；now-version / recent-errors / overview stream 都兜底
        if (url.includes("/stats/now-version")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: {
                  version: "0.13.0",
                  client_ip_count: 42,
                  total_requests: 1024,
                  error_count: 0,
                  total_tokens_input: 100_000,
                  total_tokens_output: 50_000,
                  tokens_by_model: [
                    { name: "gpt-4o", input: 60_000, output: 40_000 },
                    { name: "claude-3.5", input: 50_000, output: 30_000 },
                  ],
                },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        // Token 趋势：真实分桶时序形状 {points:[{t,in,out}]}
        if (url.includes("/stats/token-trends")) {
          const end = Date.now();
          const points = Array.from({ length: 28 }).map((_, i) => ({
            t: end - (27 - i) * 6 * 3600_000,
            in: 12_000 + i * 100,
            out: 5_000 + i * 50,
          }));
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { points },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        // 其他接口返空对象（TanStack Query 不允许 undefined）
        return Promise.resolve(
          new Response(JSON.stringify({ code: 200, message: "success", data: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        );
      }),
    );
  });

  it("后端 name/input/output 用量可渲染到模型 Top", async () => {
    const Wrapper = makeWrapper();
    render(<DashboardPage />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText(/gpt-4o/)).toBeInTheDocument();
      expect(screen.getAllByText(/10万|100K/).length).toBeGreaterThan(0);
    });
  });

  it("错误数为 0 时错误数卡用 neutral 色", async () => {
    const Wrapper = makeWrapper();
    render(<DashboardPage />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("错误数")).toBeInTheDocument();
    });
    // neutral 调色板对应 text-ink-muted
    const card = screen.getByText("错误数").closest("div")?.parentElement;
    const iconWrap = card?.parentElement?.querySelector(".text-ink-muted");
    expect(iconWrap).toBeTruthy();
  });
});

describe("<DashboardPage /> Token 趋势", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/stats/now-version")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: {
                  version: "0.13.0",
                  client_ip_count: 42,
                  total_requests: 1024,
                  error_count: 0,
                  total_tokens_input: 100_000,
                  total_tokens_output: 50_000,
                  tokens_by_model: [{ name: "gpt-4o", input: 60_000, output: 40_000 }],
                },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        if (url.includes("/stats/token-trends")) {
          const end = Date.now();
          const points = Array.from({ length: 28 }).map((_, i) => ({
            t: end - (27 - i) * 6 * 3600_000,
            in: 12_000 + i * 100,
            out: 5_000 + i * 50,
          }));
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { points },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ code: 200, message: "success", data: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        );
      }),
    );
  });

  it("趋势接口返回真实点位后渲染两条折线，不再显示预览标注", async () => {
    const Wrapper = makeWrapper();
    render(<DashboardPage />, { wrapper: Wrapper });
    await waitFor(() => {
      // 真实数据渲染出两条折线
      const paths = document.querySelectorAll("path");
      expect(paths.length).toBeGreaterThanOrEqual(2);
    });
    // 预览时代的角标与 tooltip 提示已随真实数据下线
    expect(screen.queryByText("预览曲线")).toBeNull();
    expect(screen.queryByText(/暂无历史分桶数据/)).toBeNull();
    expect(screen.queryByText(/仅作界面预览/)).toBeNull();
  });

  it("趋势接口返回空点位时显示暂无用量数据占位", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/stats/now-version")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: {
                  version: "0.13.0",
                  client_ip_count: 1,
                  total_requests: 1,
                  error_count: 0,
                  total_tokens_input: 0,
                  total_tokens_output: 0,
                  tokens_by_model: [],
                },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        if (url.includes("/stats/token-trends")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { points: [] },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ code: 200, message: "success", data: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        );
      }),
    );
    const Wrapper = makeWrapper();
    render(<DashboardPage />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("暂无用量数据")).toBeInTheDocument();
    });
    expect(screen.getByText("暂无模型用量")).toBeInTheDocument();
  });

  it("总览统计失败时模型 Top 显示加载失败，而不是暂无模型用量", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/stats/now-version")) {
          return Promise.resolve(
            new Response(JSON.stringify({ code: 500, message: "boom", data: null }), {
              status: 500,
              headers: { "content-type": "application/json" },
            }),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ code: 200, message: "success", data: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        );
      }),
    );
    const Wrapper = makeWrapper();
    render(<DashboardPage />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getAllByText("加载失败，请检查网络后重试").length).toBeGreaterThan(0);
    });
    expect(screen.queryByText("暂无模型用量")).not.toBeInTheDocument();
  });
});
