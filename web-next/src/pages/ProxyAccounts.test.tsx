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

function eligibleChannel(overrides: Partial<Channel>): Channel {
  return {
    ...sampleChannel,
    id: 7,
    name: "OpenCode Free",
    enabled: true,
    is_free: true,
    opencode_compat: true,
    base_url: "https://opencode.ai/inference",
    channel_proxy: "socks5h://Default.{account}:token@resin.test:2260",
    models: [{ id: 70, channel_id: 7, name: "model-a", source: "auto" }],
    ...overrides,
  };
}

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

const ledger = {
  channel_id: 7,
  items: [
    {
      account: "opencode0a1b2c3d",
      state: "usable",
      outcome: "healthy",
      last_probe_status: 200,
      last_probe_at: "2026-10-24T12:00:00Z",
      last_success_at: "2026-10-24T12:00:00Z",
      cooldown_until: "0001-01-01T00:00:00Z",
      source: "generated",
    },
    {
      account: "opencode0e4f5a6b",
      state: "unusable",
      outcome: "rejected",
      last_probe_status: 403,
      last_probe_at: "2026-10-24T12:00:00Z",
      last_success_at: "0001-01-01T00:00:00Z",
      cooldown_until: "2026-10-25T12:00:00Z",
      source: "generated",
    },
    {
      account: "opencode0c7d8e9f",
      state: "untested",
      outcome: "",
      last_probe_status: 0,
      last_probe_at: "0001-01-01T00:00:00Z",
      last_success_at: "0001-01-01T00:00:00Z",
      cooldown_until: "0001-01-01T00:00:00Z",
      source: "generated",
    },
  ],
  total: 3,
  usable: 1,
  untested: 1,
  unusable: 1,
};

describe("<ProxyAccountsPage /> 账号体检", () => {
  it("仅渲染合格渠道的独立卡片，全部随机生成无自定义列表", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({}), plainChannel]);
      if (url.includes("/channel/proxy-accounts?")) return jsonResponse(ledger);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: true });
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
    expect(screen.queryByPlaceholderText(/每行一个账号/)).not.toBeInTheDocument();
    // 打开页面即有台账, 无需先体检
    expect(await screen.findByText("opencode0a1b2c3d")).toBeInTheDocument();
  });

  it("Free 与 Old 各自独立记账号，互不混账", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(report);
      }
      if (url.includes("/channel/list")) {
        return jsonResponse([
          eligibleChannel({}),
          eligibleChannel({ id: 9, name: "OpenCode Free Old", base_url: "https://opencode.ai/zen" }),
        ]);
      }
      if (url.includes("/channel/proxy-accounts?")) {
        return jsonResponse({ ...ledger, items: [], total: 0, usable: 0, untested: 0, unusable: 0 });
      }
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    expect(await screen.findByText("OpenCode Free")).toBeInTheDocument();
    expect(screen.getByText("OpenCode Free Old")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "开始体检" })).toHaveLength(2);

    const user = userEvent.setup();
    const buttons = screen.getAllByRole("button", { name: "开始体检" });
    await user.click(buttons[1]);
    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0].channel_id).toBe(9);
  });

  it("点击开始体检后按默认参数请求并渲染逐账号结论", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe")) {
        return jsonResponse(report);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      if (url.includes("/channel/proxy-accounts?")) return jsonResponse(ledger);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
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

    expect(await screen.findAllByText("opencode0a1b2c3d")).toHaveLength(2);
    expect(screen.getByText("健康 1")).toBeInTheDocument();
    expect(screen.getByText("被拒 1")).toBeInTheDocument();
    expect(screen.getByText("失败 1")).toBeInTheDocument();
    expect(screen.getByText("FreeTierError: forbidden")).toBeInTheDocument();
    expect(screen.getByText("探测模型：model-a")).toBeInTheDocument();
  });

  it("台账展示三态、筛选与开关自动体检", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    let ledgerEnabled = true;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) {
        const filter = /filter=(\w+)/.exec(url)?.[1] ?? "all";
        const items = ledger.items.filter((item) => filter === "all" || item.state === filter);
        return jsonResponse({
          ...ledger,
          items,
          total: items.length,
          usable: items.filter((item) => item.state === "usable").length,
          untested: items.filter((item) => item.state === "untested").length,
          unusable: items.filter((item) => item.state === "unusable").length,
        });
      }
      if (url.includes("/channel/proxy-accounts/auto-probe")) {
        if (init?.method === "POST") {
          probeBodies.push(JSON.parse(String(init.body ?? "{}")));
          return jsonResponse({ enabled: !ledgerEnabled });
        }
        return jsonResponse({ enabled: ledgerEnabled });
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("opencode0a1b2c3d");
    expect(await screen.findByText("可用 1")).toBeInTheDocument();
    expect(screen.getByText("未测试 1")).toBeInTheDocument();
    expect(screen.getByText("不可用 1")).toBeInTheDocument();
    expect(screen.getByText(/冷却至/)).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByText("未测试 1"));
    expect(await screen.findByText("opencode0c7d8e9f")).toBeInTheDocument();
    expect(screen.queryByText("opencode0a1b2c3d")).not.toBeInTheDocument();

    await user.click(screen.getByRole("switch"));
    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({ enabled: false });
  });

  it("台账删除账号后刷新列表", async () => {
    const deleteBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) return jsonResponse(ledger);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      if (url.includes("/channel/proxy-accounts")) {
        deleteBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse({ deleted: 1 });
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("opencode0a1b2c3d");
    const user = userEvent.setup();
    await user.click(screen.getAllByRole("button", { name: "删除" })[0]);
    await user.click(screen.getByRole("button", { name: /再次点击确认/ }));
    await waitFor(() => expect(deleteBodies).toHaveLength(1));
    expect(deleteBodies[0]).toEqual({ channel_id: 7, accounts: ["opencode0a1b2c3d"] });
  });

  it("没有合格渠道时显示空态", async () => {
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
