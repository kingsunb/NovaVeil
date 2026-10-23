import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";

import ProxyAccountsPage from "./ProxyAccounts";
import { sampleChannel } from "@/test/fixtures/channels";
import type { Channel } from "@/lib/types";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  localStorage.setItem(
    "nv-auth",
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

function okResponse(data: unknown): Response {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function jsonResponse(data: unknown) {
  return Promise.resolve(okResponse(data));
}

const eligibleChannel: Channel = {
  ...sampleChannel,
  id: 7,
  name: "OpenCode Free",
  enabled: true,
  is_free: true,
  opencode_compat: true,
  base_url: "https://opencode.ai/inference",
  channel_proxy: "http://{account}:pass@resin.test:2260",
  models: [{ id: 70, channel_id: 7, name: "model-a", source: "auto" }],
};

const plainChannel: Channel = {
  ...sampleChannel,
  id: 8,
  name: "Plain Channel",
  enabled: true,
  opencode_compat: false,
  base_url: "https://api.example.com/v1",
};

const report = {
  channel_id: 7,
  model: "model-a",
  results: [
    { account: "opencode0a1b2c3d", outcome: "healthy", status: 200, detail: "", latency_ms: 120 },
    {
      account: "opencode0e4f5a6b",
      outcome: "rejected",
      status: 403,
      detail: "FreeTierError: forbidden",
      latency_ms: 90,
    },
    { account: "opencode0c7d8e9f", outcome: "error", status: 502, detail: "502", latency_ms: 60 },
  ],
  summary: { total: 3, healthy: 1, rejected: 1, failed: 1 },
};

describe("<ProxyAccountsPage /> 账号体检", () => {
  it("仅列出启用中且代理含 {account} 占位符的免费档渠道", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel, plainChannel]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    expect(await screen.findByText("OpenCode Free")).toBeInTheDocument();
    expect(screen.queryByText("Plain Channel")).not.toBeInTheDocument();
    expect(screen.getByDisplayValue("100")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "开始体检" })).toBeInTheDocument();
  });

  it("点击开始体检后按默认参数请求并渲染逐账号结论", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(report);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("OpenCode Free");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "开始体检" }));

    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({ channel_id: 7, count: 100, concurrency: 10 });

    expect(await screen.findByText("opencode0a1b2c3d")).toBeInTheDocument();
    expect(screen.getByText("opencode0e4f5a6b")).toBeInTheDocument();
    expect(screen.getByText("opencode0c7d8e9f")).toBeInTheDocument();
    expect(screen.getByText("健康 1")).toBeInTheDocument();
    expect(screen.getByText("被拒 1")).toBeInTheDocument();
    expect(screen.getByText("失败 1")).toBeInTheDocument();
    expect(screen.getByText("FreeTierError: forbidden")).toBeInTheDocument();
    expect(screen.getByText("探测模型：model-a")).toBeInTheDocument();
  });

  it("粘贴自定义账号后按列表体检且数量输入失效", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(report);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("OpenCode Free");
    const user = userEvent.setup();
    const textarea = screen.getByPlaceholderText(/每行一个账号/);
    await user.type(textarea, "acct_one, acct_two");
    expect(screen.getByDisplayValue("100")).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "开始体检" }));
    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({
      channel_id: 7,
      accounts: ["acct_one", "acct_two"],
      concurrency: 10,
    });
  });

  it("没有合格渠道时显示空态且无开始按钮", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/channel/list")) return jsonResponse([plainChannel]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    expect(await screen.findByText(/没有可体检的渠道/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "开始体检" })).not.toBeInTheDocument();
  });
});
