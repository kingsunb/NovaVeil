import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import ModelEvalPage from "./ModelEval";
import { sampleChannel } from "@/test/fixtures/channels";
import type { Channel } from "@/lib/types";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/?view=current"]}>
        {children}
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  localStorage.setItem(
    "nv-auth",
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

/** 仅拦截「当前评估」视图所需接口，其余兜底空数据。 */
function mockEvalApis(channels: Channel[], stats: Array<{ channel_id: number; model_name: string; total_count: number; success_count: number }> = []) {
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    if (url.includes("/channel/list")) {
      return Promise.resolve(
        new Response(JSON.stringify({ code: 200, message: "success", data: channels }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    }
    if (url.includes("/model-eval/stats")) {
      return Promise.resolve(
        new Response(JSON.stringify({ code: 200, message: "success", data: { items: stats } }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    }
    return Promise.resolve(
      new Response(JSON.stringify({ code: 200, message: "success", data: null }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
  }));
}

function channelWith(name: string, id: number, sort: number): Channel {
  return {
    ...sampleChannel,
    id,
    name,
    sort,
    enabled: true,
    models: [{ id: id * 100, channel_id: id, name: `${name}-model`, source: "auto" }],
  };
}

describe("<ModelEvalPage /> 当前评估渠道排序", () => {
  it("渠道按 sort 降序排列（优先级高在上），与渠道列表自定义排序一致", async () => {
    // 后端按 id 顺序返回（乱序），前端应按 sort 降序重排为 high(30) → mid(20) → low(10)。
    mockEvalApis([
      channelWith("ch-low", 1, 10),
      channelWith("ch-high", 2, 30),
      channelWith("ch-mid", 3, 20),
    ]);
    render(<ModelEvalPage />, { wrapper: Wrapper });

    // 等待三个渠道折叠按钮渲染（每渠道 1 个模型，默认展开态 aria-label 以「折叠 」开头）。
    await waitFor(() =>
      expect(screen.getAllByRole("button", { name: /^折叠 / })).toHaveLength(3),
    );

    const order = screen
      .getAllByRole("button", { name: /^折叠 / })
      .map((button) => button.getAttribute("aria-label"));
    expect(order).toEqual(["折叠 ch-high", "折叠 ch-mid", "折叠 ch-low"]);
  });

  it("当前评估显示一键评估按钮与评估设置", async () => {
    mockEvalApis([
      { ...channelWith("free", 1, 10), is_free: true },
      { ...channelWith("paid", 2, 20), is_free: false },
    ]);
    render(<ModelEvalPage />, { wrapper: Wrapper });

    await waitFor(() => expect(screen.getByRole("button", { name: /一键评估免费渠道所有模型/ })).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /一键评估未评估模型/ })).toBeInTheDocument();
    expect(screen.getByLabelText("已评估成功模型不再评估")).not.toBeChecked();
    expect(screen.getByLabelText("已评估失败模型不再评估")).not.toBeChecked();
    expect(screen.getByLabelText("只评估未评估模型")).not.toBeChecked();
  });

  it("评估设置过滤免费渠道一键评估候选", async () => {
    mockEvalApis(
      [
        { ...channelWith("free", 1, 10), is_free: true },
        { ...channelWith("paid", 2, 20), is_free: false },
      ],
      [{ channel_id: 1, model_name: "free-model", total_count: 1, success_count: 1 }],
    );
    render(<ModelEvalPage />, { wrapper: Wrapper });

    await waitFor(() => expect(screen.getByRole("button", { name: "一键评估免费渠道所有模型 (1)" })).toBeEnabled());
    expect(screen.getByRole("button", { name: "一键评估未评估模型 (1)" })).toBeEnabled();

    await userEvent.setup().click(screen.getByLabelText("已评估成功模型不再评估"));
    await waitFor(() => expect(screen.getByRole("button", { name: "一键评估免费渠道所有模型 (0)" })).toBeDisabled());
    expect(screen.getByRole("button", { name: "一键评估未评估模型 (1)" })).toBeEnabled();
  });
});