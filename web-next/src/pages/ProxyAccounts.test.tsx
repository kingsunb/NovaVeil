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

/** 后台体检作业: 启动只回 job_id, 状态由轮询接口取。 */
const jobStart = {
  job_id: "probe-0123",
  channel_id: 7,
  model: "model-a",
  state: "running",
  total: 3,
};

const jobDone = {
  ...jobStart,
  state: "done",
  probed: 3,
  healthy: 1,
  rejected: 1,
  failed: 1,
  started_at: "2026-10-26T04:00:00Z",
  finished_at: "2026-10-26T04:00:05Z",
  results: report.results,
  summary: report.summary,
};

const ledgerNow = new Date().toISOString();

const ledger = {
  channel_id: 7,
  items: [
    {
      account: "opencode0a1b2c3d",
      state: "usable",
      outcome: "healthy",
      last_probe_status: 200,
      last_probe_at: ledgerNow,
      last_success_at: "2026-10-24T12:00:00Z",
      cooldown_until: "0001-01-01T00:00:00Z",
      source: "generated",
      success_count: 12,
      real_success_count: 10,
      real_failure_count: 2,
      probe_success_count: 2,
      success_days: 3,
    },
    {
      account: "opencode0e4f5a6b",
      state: "unusable",
      outcome: "rejected",
      last_probe_status: 403,
      last_probe_at: "2026-10-24T12:00:00Z",
      last_success_at: "2026-10-23T12:00:00Z",
      cooldown_until: "2026-10-25T12:00:00Z",
      source: "generated",
      success_count: 5,
      real_success_count: 3,
      real_failure_count: 1,
      probe_success_count: 2,
      success_days: 2,
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
      success_count: 0,
      real_success_count: 0,
      real_failure_count: 0,
      probe_success_count: 0,
      success_days: 0,
    },
    {
      account: "opencode0d9e0f1a2",
      state: "error",
      outcome: "error",
      last_probe_status: 502,
      last_probe_at: "2026-10-24T12:00:00Z",
      last_success_at: "0001-01-01T00:00:00Z",
      cooldown_until: "0001-01-01T00:00:00Z",
      source: "generated",
      success_count: 0,
      real_success_count: 0,
      real_failure_count: 0,
      probe_success_count: 0,
      success_days: 0,
    },
  ],
  total: 4,
  usable: 1,
  untested: 3,
  unusable: 1,
  probe_error: 1,
  historic: 2,
};

function isTestedToday(item: (typeof ledger.items)[number]) {
  const now = new Date();
  const sameLocalDay = (value: string) => {
    const at = new Date(value);
    return (
      at.getFullYear() === now.getFullYear() &&
      at.getMonth() === now.getMonth() &&
      at.getDate() === now.getDate()
    );
  };
  return sameLocalDay(item.last_probe_at) || sameLocalDay(item.last_success_at);
}

/** 按台账接口口径过滤, 供 fetch mock 复用。 */
function ledgerFor(url: string) {
  const filter = /filter=(\w+)/.exec(url)?.[1] ?? "all";
  const hasSuccess = (item: (typeof ledger.items)[number]) => item.success_count > 0;
  const items = ledger.items.filter((item) => {
    if (filter === "all") return true;
    if (filter === "historic") return hasSuccess(item);
    if (filter === "untested") return !isTestedToday(item);
    return item.state === filter;
  });
  return jsonResponse({
    ...ledger,
    items,
    total: ledger.items.length,
    usable: ledger.items.filter((item) => item.state === "usable").length,
    untested: ledger.items.filter((item) => !isTestedToday(item)).length,
    unusable: ledger.items.filter((item) => item.state === "unusable").length,
    probe_error: ledger.items.filter((item) => item.state === "error").length,
    historic: ledger.items.filter(hasSuccess).length,
  });
}

describe("<ProxyAccountsPage /> 账号体检", () => {
  it("下拉切换渠道后展示所选渠道的台账", async () => {
    const ledgerUrls: string[] = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) {
        ledgerUrls.push(url);
        return ledgerFor(url);
      }
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: true });
      if (url.includes("/channel/list")) {
        return jsonResponse([
          eligibleChannel({}),
          eligibleChannel({ id: 9, name: "OpenCode Free Old", base_url: "https://opencode.ai/zen" }),
          eligibleChannel({
            id: 10,
            name: "OpenCode",
            is_free: false,
            base_url: "https://opencode.ai/zen/v1",
          }),
        ]);
      }
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    // 默认选第一个渠道, 台账立即展示
    expect(await screen.findByText("opencode0a1b2c3d")).toBeInTheDocument();
    expect(screen.getByText("https://opencode.ai/inference")).toBeInTheDocument();
    expect(screen.queryByText("Plain Channel")).not.toBeInTheDocument();
    // 官方付费 OpenCode 不进账号池体检
    expect(screen.queryByRole("option", { name: "OpenCode" })).not.toBeInTheDocument();

    // 切到 Old → 展示 Old 的台账
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("体检渠道"), "9");
    expect(await screen.findByText("https://opencode.ai/zen")).toBeInTheDocument();
    await waitFor(() => {
      expect(ledgerUrls.some((url) => url.includes("channel_id=9"))).toBe(true);
    });
  });

  it("点击开始体检后按默认参数请求并渲染逐账号结论", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe/")) return jsonResponse(jobDone);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(jobStart);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findAllByText("OpenCode Free");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "开始体检" }));

    expect(await screen.findAllByText("opencode0a1b2c3d")).toHaveLength(2);
    expect(screen.getByText("健康 1")).toBeInTheDocument();
    expect(screen.getByText("被拒 1")).toBeInTheDocument();
    expect(screen.getByText("失败 1")).toBeInTheDocument();
    expect(screen.getByText("FreeTierError: forbidden")).toBeInTheDocument();
    expect(probeBodies[0]).toEqual({ channel_id: 7, count: 100, concurrency: 10, model: "model-a" });
  });

  it("生成数量上限 30000", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe/")) return jsonResponse(jobDone);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(jobStart);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findAllByText("OpenCode Free");
    const user = userEvent.setup();
    await user.clear(screen.getByDisplayValue("100"));
    await user.type(screen.getByDisplayValue(""), "30000");
    await user.click(screen.getByRole("button", { name: "开始体检" }));

    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({ channel_id: 7, count: 30000, concurrency: 10, model: "model-a" });
  });

  it("一键测试台账全部账号并展示进度结果", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe/")) return jsonResponse(jobDone);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(jobStart);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("opencode0a1b2c3d");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "一键测试台账" }));

    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({
      channel_id: 7,
      accounts: [
        "opencode0a1b2c3d",
        "opencode0e4f5a6b",
        "opencode0c7d8e9f",
        "opencode0d9e0f1a2",
      ],
      concurrency: 10,
      model: "model-a",
    });
    expect(await screen.findByText("健康 1")).toBeInTheDocument();
  });

  it("台账四态筛选、勾选账号与开关自动体检", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    const ledgerEnabled = true;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe/")) return jsonResponse(jobDone);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(jobStart);
      }
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
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
    expect(screen.getByText("今日未测试 3")).toBeInTheDocument();
    expect(screen.getByText("不可用 1")).toBeInTheDocument();
    expect(screen.getByText("网络错误 1")).toBeInTheDocument();
    expect(screen.getByText(/冷却至/)).toBeInTheDocument();

    const user = userEvent.setup();
    // 勾选不可用与网络错误两个账号 → 一键测试只测勾选的
    await user.click(screen.getByLabelText(/选择账号 opencode0e4f5a6b/));
    await user.click(screen.getByLabelText(/选择账号 opencode0d9e0f1a2/));
    await user.click(screen.getByRole("button", { name: "测试已选 2 个账号" }));
    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toEqual({
      channel_id: 7,
      accounts: ["opencode0e4f5a6b", "opencode0d9e0f1a2"],
      concurrency: 10,
      model: "model-a",
    });

    // 网络错误筛选: 台账只剩网络错误账号(结果表仍展示批量体检结果)
    await user.click(screen.getByText("网络错误 1"));
    expect(await screen.findByText("opencode0d9e0f1a2")).toBeInTheDocument();
    expect(screen.getAllByText("opencode0a1b2c3d")).toHaveLength(1);

    // 今日未测试筛选: 今日无体检且无成功的账号(含冷却与旧体检), 今日已测的可用账号不在台账
    await user.click(screen.getByText("今日未测试 3"));
    expect(await screen.findByText("opencode0c7d8e9f")).toBeInTheDocument();
    expect(screen.getAllByText("opencode0a1b2c3d")).toHaveLength(1);

    // 历史可用筛选: 只显示曾成功过的账号(含冷却中的好号), 未成功账号不出现在台账
    expect(screen.getByText("历史可用 2")).toBeInTheDocument();
    await user.click(screen.getByText("历史可用 2"));
    await waitFor(() =>
      expect(screen.getAllByText("opencode0e4f5a6b")).toHaveLength(2),
    );
    expect(screen.queryByText("opencode0d9e0f1a2")).not.toBeInTheDocument();

    // 开关自动体检
    await user.click(screen.getByRole("switch"));
    await waitFor(() => expect(probeBodies.length).toBeGreaterThanOrEqual(2));
    expect(probeBodies[probeBodies.length - 1]).toEqual({ enabled: false });
  });

  it("台账删除账号后刷新列表", async () => {
    const deleteBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
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

  it("生成数量非法不阻塞一键测试", async () => {
    const probeBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts/probe/")) return jsonResponse(jobDone);
      if (url.includes("/channel/proxy-accounts/probe")) {
        probeBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse(jobStart);
      }
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    await screen.findByText("opencode0a1b2c3d");
    const user = userEvent.setup();
    // 一键测试只测台账账号, 生成数量与它无关: 非法值不应阻塞。
    const countInput = screen
      .getAllByDisplayValue("100")
      .find((element) => element instanceof HTMLInputElement);
    if (!countInput) throw new Error("生成数量输入框应存在");
    await user.clear(countInput);
    await user.type(countInput, "0");
    await user.click(screen.getByRole("button", { name: "一键测试台账" }));

    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toMatchObject({ channel_id: 7, concurrency: 10 });
  });

  it("勾选账号后可批量删除已选", async () => {
    const deleteBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) return ledgerFor(url);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      if (url.includes("/channel/proxy-accounts")) {
        deleteBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return jsonResponse({ deleted: 2 });
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
    await user.click(screen.getByLabelText(/选择账号 opencode0a1b2c3d/));
    await user.click(screen.getByLabelText(/选择账号 opencode0e4f5a6b/));

    await user.click(screen.getByRole("button", { name: "删除已选 2 个账号" }));
    await user.click(screen.getByRole("button", { name: /再次点击确认/ }));
    await waitFor(() => expect(deleteBodies).toHaveLength(1));
    expect(deleteBodies[0]).toEqual({
      channel_id: 7,
      accounts: ["opencode0a1b2c3d", "opencode0e4f5a6b"],
    });
    // 删除成功后选择集与按钮一并清空。
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /删除已选/ })).not.toBeInTheDocument(),
    );
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

  it("台账分页: 默认 100 条一页, 可翻页并调整每页条数", async () => {
    const bigLedger = {
      channel_id: 7,
      items: Array.from({ length: 250 }, (_, index) => ({
        account: `opencode${String(index).padStart(12, "0")}`,
        state: index % 4 === 0 ? "usable" : index % 4 === 1 ? "untested" : index % 4 === 2 ? "unusable" : "error",
        outcome: index % 4 === 1 ? "" : "healthy",
        last_probe_status: index % 4 === 1 ? 0 : 200,
        last_probe_at: "2026-10-24T12:00:00Z",
        last_success_at: "0001-01-01T00:00:00Z",
        cooldown_until: "0001-01-01T00:00:00Z",
        source: "generated",
        success_count: index,
        real_success_count: index,
        real_failure_count: 0,
        probe_success_count: 0,
        success_days: index % 5,
      })),
      total: 250,
      usable: 63,
      untested: 63,
      unusable: 62,
      probe_error: 62,
      historic: 63,
    };
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/channel/proxy-accounts?")) return jsonResponse(bigLedger);
      if (url.includes("/channel/proxy-accounts/auto-probe")) return jsonResponse({ enabled: false });
      if (url.includes("/channel/list")) return jsonResponse([eligibleChannel({})]);
      return jsonResponse(null);
    }));

    render(
      <Wrapper>
        <ProxyAccountsPage />
      </Wrapper>,
    );

    // 首页 100 条, 第 1/3 页
    expect((await screen.findAllByLabelText(/选择账号 opencode/))).toHaveLength(100);
    expect(await screen.findByText("第 1 / 3 页")).toBeInTheDocument();

    // 下一页 → 101-200 条
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(await screen.findByText("第 2 / 3 页")).toBeInTheDocument();
    expect(screen.getByText("opencode000000000100")).toBeInTheDocument();
    expect(screen.queryByText("opencode000000000000")).not.toBeInTheDocument();

    // 每页条数调到 5000 → 一页放下全部 250 条
    await user.selectOptions(screen.getByLabelText("每页条数"), "5000");
    expect(await screen.findByText("第 1 / 1 页")).toBeInTheDocument();
    expect(screen.getAllByLabelText(/选择账号 opencode/)).toHaveLength(250);
    expect(screen.getByText("opencode000000000000")).toBeInTheDocument();

    // 全选只作用于当前页
    await user.click(screen.getByLabelText("全选当前筛选结果"));
    expect(screen.getByRole("button", { name: "测试已选 250 个账号" })).toBeInTheDocument();
  });
});
