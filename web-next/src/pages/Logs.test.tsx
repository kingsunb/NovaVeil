import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import LogsPage from "./Logs";
import { formatDuration, formatElapsedWithFirst } from "@/lib/utils";
import { ThemeProvider } from "@/components/layout/ThemeProvider";

// jsdom 无真实布局，@tanstack/react-virtual 测不出容器高度、恒渲染 0 行，
// 追踪 Sheet 无法经「行点击」打开。单测把虚拟化器 mock 成全量渲染；
// 行级虚拟化/滚动语义仍由 e2e/axe（真实 Chromium）覆盖。
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (opts: { count: number; estimateSize?: () => number }) => {
    const rowHeight = opts.estimateSize?.() ?? 44;
    return {
      getTotalSize: () => opts.count * rowHeight,
      getVirtualItems: () =>
        Array.from({ length: opts.count }, (_, index) => ({
          index,
          start: index * rowHeight,
          size: rowHeight,
          key: index,
        })),
      measureElement: () => {},
    };
  },
}));

const STORAGE_KEY = "nv-auth";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          {children}
        </MemoryRouter>
      </QueryClientProvider>
    </ThemeProvider>
  );
}

function makeLog(i: number) {
  const base = Date.now() - i * 1000;
  const startedAt = base;
  // 首字耗时固定 100ms：success/failed（已提交）首字已到达，running（首字未到）不设。
  // duration_ms 为定稿总耗时（毫秒），首字耗时须小于总耗时，故 success 取 110ms 总 / 100ms 首字。
  const isRunning = i % 3 === 2;
  const firstElapsedMs = isRunning ? 0 : 100; // running 首字未到 → 0
  const hasFirstToken = !isRunning;
  // first_token_at 须为 ISO 字符串：首字时点 = 起始时刻 + 首字耗时（毫秒）。
  const firstTokenAt = hasFirstToken
    ? new Date(startedAt + firstElapsedMs).toISOString()
    : undefined;
  return {
    id: i,
    status: i % 3 === 0 ? "failed" : i % 3 === 1 ? "success" : "running",
    started_at: new Date(startedAt).toISOString(),
    first_token_at: firstTokenAt,
    duration_ms: 100 + i * 10,
    model: `gpt-4o-${i}`,
    client_ip: `10.0.0.${i}`,
    api_key: `sk-Nv…${i.toString().padStart(4, "0")}`,
    key_name: `key-${i}`,
    usage: {
      prompt_tokens: 100 + i,
      completion_tokens: 50 + i,
      total_tokens: 150 + i * 2,
    },
    usage_estimated: false,
    round: i % 5,
    target_channel: `channel-${i % 4}`,
    target_model: `gpt-4o-real-${i}`,
    client_format: "openai_chat",
    upstream_type: "openai",
    relay_mode: i % 2 === 0 ? "passthrough" : "converted",
    sending: i % 3 === 2,
    attempts: [],
  };
}

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function setupBasicFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url.includes("/log/overview")) return Promise.resolve(jsonOk([]));
      if (url.includes("/log/failures")) return Promise.resolve(jsonOk([]));
      if (url.includes("/log/errors")) return Promise.resolve(jsonOk([]));
      if (url.includes("/log/stop-all-state")) {
        return Promise.resolve(jsonOk({ is_stopped: false }));
      }
      if (url.includes("/log/client-stats")) return Promise.resolve(jsonOk([]));
      return Promise.resolve(jsonOk(null));
    }),
  );
}

/**
 * FakeEventSource —— 立即 open 并不接收消息（仅建立连接）
 */
function setupEventSource() {
  const sources: FakeEventSource[] = [];
  class FakeEventSource {
    url: string;
    readyState: 0 | 1 | 2 = 0;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    onopen: ((e: Event) => void) | null = null;
    listeners: Record<string, Array<(e: Event) => void>> = {};
    constructor(url: string) {
      this.url = url;
      sources.push(this);
      // 立即 open
      queueMicrotask(() => {
        this.readyState = 1;
        this.onopen?.(new Event("open"));
      });
    }
    addEventListener(name: string, fn: (e: Event) => void) {
      const list = this.listeners[name] ?? [];
      list.push(fn);
      this.listeners[name] = list;
    }
    fireEvent(name: string, data: unknown) {
      const event = new MessageEvent(name, {
        data: JSON.stringify(data),
      });
      this.listeners[name]?.forEach((fn) => fn(event));
    }
    close() {
      this.readyState = 2;
    }
  }
  vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
  return sources;
}

beforeEach(() => {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

describe("<LogsPage /> 虚拟化结构", () => {
  it("空数据：grid 容器存在，aria-rowcount=1（表头）", async () => {
    setupBasicFetch();
    setupEventSource();
    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    const grid = await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
      return g;
    });
    // grid 含表头行 + 0 条数据行
    expect(grid.getAttribute("aria-rowcount")).toBe("1");
    // 滚动容器存在（高度 min(480px, 60vh) 由真实浏览器渲染；jsdom 的
    // cssstyle 不接受 min()，无法断言 style 属性）
    expect(grid.querySelector(".overflow-auto")).not.toBeNull();
    // 表头具备 columnheader 语义（审计 4.18）
    expect(
      grid.querySelectorAll('[role="row"][aria-rowindex="1"] > [role="columnheader"]'),
    ).toHaveLength(9);
  });

  it("SSE 推送 N 条后 grid aria-rowcount 同步（表头 + N 数据行）", async () => {
    setupBasicFetch();
    const sources = setupEventSource();

    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    const grid = await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
      return g;
    });

    // SSE onopen 触发后再发消息
    await waitFor(() => sources.length > 0 && sources[0]!.readyState !== 0);

    // 模拟 SSE 推送 5 条（用 act 包住 setState 更新避免 React 告警）
    const es = sources[0]!;
    await act(async () => {
      for (let i = 1; i <= 5; i++) {
        es.fireEvent("log", makeLog(i));
      }
    });

    // 等 React 批处理；aria-rowcount 含表头行（1 + 5）。
    // 数据行的 aria-rowindex 依赖虚拟化器真实测量容器高度，jsdom 恒为 0
    // 不渲染行 —— 行级语义由 e2e/axe（真实 Chromium）覆盖。
    await waitFor(() => {
      expect(grid.getAttribute("aria-rowcount")).toBe("6");
    });
  });
});

describe("<LogsPage /> 实时列表顺序（最新在顶）", () => {
  it("后端快照按 ID 倒序到达时，前端合并后仍保持最新(大 ID)在顶、旧序向下", async () => {
    setupBasicFetch();
    const sources = setupEventSource();

    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
      return g;
    });
    await waitFor(() => sources.length > 0 && sources[0]!.readyState !== 0);

    const es = sources[0]!;
    // 模拟后端 OpenRequestStream 快照：按请求 ID 倒序逐条下发（5 → 4 → … → 1）。
    // 回归标：此前前端对新消息直接前插，会把这段倒序快照重排成旧在上新在下。
    await act(async () => {
      for (let i = 5; i >= 1; i--) {
        es.fireEvent("log", makeLog(i));
      }
    });

    const ids = await waitFor(() => {
      const rows = Array.from(
        container.querySelectorAll('[aria-label^="查看请求 #"]'),
      );
      if (rows.length !== 5) throw new Error("rows not yet rendered");
      return rows.map((r) => r.getAttribute("aria-label"));
    });

    expect(ids).toEqual([
      "查看请求 #5 追踪",
      "查看请求 #4 追踪",
      "查看请求 #3 追踪",
      "查看请求 #2 追踪",
      "查看请求 #1 追踪",
    ]);
  });

  it("命中已有记录时原地更新，行不移动、不重复", async () => {
    setupBasicFetch();
    const sources = setupEventSource();

    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
      return g;
    });
    await waitFor(() => sources.length > 0 && sources[0]!.readyState !== 0);

    const es = sources[0]!;
    // 先推送最新一条，再推送同 ID 状态更新
    await act(async () => {
      es.fireEvent("log", makeLog(3));
    });
    await act(async () => {
      es.fireEvent("log", makeLog(3)); // 同 ID 更新，行数不变
    });

    const labels = await waitFor(() => {
      const rows = Array.from(container.querySelectorAll('[aria-label^="查看请求 #"]'));
      if (rows.length !== 1) throw new Error("row not yet rendered");
      return rows.map((r) => r.getAttribute("aria-label"));
    });
    expect(labels.length).toBe(1);
    expect(labels[0]).toBe("查看请求 #3 追踪");
  });

  it("代理列：有 proxy_addr 显示「代理」标签，无则显示「直连」", async () => {
    setupBasicFetch();
    const sources = setupEventSource();

    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
      return g;
    });
    await waitFor(() => sources.length > 0 && sources[0]!.readyState !== 0);

    const es = sources[0]!;
    // 推送两条：一条有代理、一条无代理
    await act(async () => {
      es.fireEvent("log", { ...makeLog(1), proxy_addr: "socks5://***@10.0.0.9:1080" });
      es.fireEvent("log", makeLog(2)); // 无 proxy_addr
    });

    const rows = await waitFor(() => {
      const r = Array.from(container.querySelectorAll('[aria-label^="查看请求 #"]'));
      if (r.length !== 2) throw new Error("rows not yet rendered");
      return r;
    });

    // 行按 ID 降序排列：#2（无代理）在前，#1（有代理）在后
    const row1 = rows[1] as HTMLElement; // #1 有代理
    const row2 = rows[0] as HTMLElement; // #2 无代理

    // 有代理的行应包含「代理」Pill
    expect(within(row1).getByText("代理")).toBeTruthy();
    // 无代理的行应包含「直连」文本
    expect(within(row2).getByText("直连")).toBeTruthy();
  });
});

describe("<LogsPage /> 清空错误日志的范围告知", () => {
  it("确认对话框必须声明清空「全部」（与筛选无关），不得按筛选子集计数表述", async () => {
    setupBasicFetch();
    setupEventSource();
    const { container } = render(<LogsPage />, { wrapper: Wrapper });

    // 切到「错误日志」Tab（Radix Dialog 走 portal，需在 document 上查）
    const errTab = Array.from(container.querySelectorAll("button")).find(
      (b) => b.textContent === "错误日志",
    );
    expect(errTab).toBeTruthy();
    fireEvent.click(errTab!);

    const clearBtn = await waitFor(() => {
      const b = Array.from(container.querySelectorAll("button")).find((btn) =>
        btn.textContent?.includes("清空错误日志"),
      );
      if (!b) throw new Error("clear button not yet");
      return b;
    });
    fireEvent.click(clearBtn);

    await waitFor(() => {
      const dialog = document.querySelector('[role="dialog"]');
      if (!dialog) throw new Error("dialog not yet");
      const text = dialog.textContent ?? "";
      // 后端 DELETE /log/errors 无条件清全库（ErrorLogDeleteAll）：
      // 文案必须如实告知「全部 + 与筛选无关」，禁止「当前筛选结果有 N 条」误导
      expect(text).toContain("全部错误日志");
      expect(text).toContain("与当前筛选无关");
      expect(text).not.toContain("当前筛选结果有");
    });
  });
});

describe("formatDuration", () => {
  it("运行中请求按 started_at 派生 elapsed", () => {
    const now = Date.parse("2024-01-01T00:01:30Z");
    const running = {
      status: "running",
      started_at: "2024-01-01T00:00:00Z",
      duration: 0,
      duration_ms: 0,
    } as const;
    expect(formatDuration(running, now)).toBe("正在请求 · 1m30s");
  });

  it("终态请求优先使用 duration_ms", () => {
    const now = Date.parse("2024-01-01T00:01:30Z");
    const finished = {
      status: "success",
      started_at: "2024-01-01T00:00:00Z",
      duration: 1_500_000,
      duration_ms: 1500,
    } as const;
    expect(formatDuration(finished, now)).toBe("1500ms");
  });

  it("started_at 非法时运行中请求仍显示正在请求", () => {
    const running = {
      status: "committed",
      started_at: "not-a-date",
      duration: 0,
      duration_ms: 0,
    } as const;
    expect(formatDuration(running, Date.now())).toBe("正在请求");
  });
});

describe("formatElapsedWithFirst", () => {
  it("运行中请求回退为总耗时展示（与旧 formatDuration 一致，向后兼容）", () => {
    const now = Date.parse("2024-01-01T00:01:30Z");
    const running = {
      status: "running",
      started_at: "2024-01-01T00:00:00Z",
      duration_ms: 0,
    } as const;
    expect(formatElapsedWithFirst(running, now)).toBe("正在请求 · 1m30s");
  });

  it("终态请求展示「首字 · 总耗时」，首字耗时为 0 时回退纯总耗时", () => {
    const now = Date.parse("2024-01-01T00:01:30Z");
    const finished = {
      status: "success",
      started_at: "2024-01-01T00:00:00Z",
      first_token_at: new Date("2024-01-01T00:00:00.100Z").toISOString(),
      duration_ms: 110,
    } as const;
    expect(formatElapsedWithFirst(finished, now)).toBe("首字 100ms · 总耗时 110ms");

    // 旧数据无 first_token_at（首字未到），回退为纯总耗时展示。
    const legacy = {
      status: "failed",
      started_at: "2024-01-01T00:00:00Z",
      duration_ms: 110,
    } as const;
    expect(formatElapsedWithFirst(legacy, now)).toBe("110ms");
  });

  it("committed 未定稿时总耗时从请求到达开始计算，首字耗时仍为 first_token_at - started_at", () => {
    const now = Date.parse("2024-01-01T00:00:01Z"); // 距起始 1s（1000ms）
    const committed = {
      status: "committed",
      started_at: "2024-01-01T00:00:00Z",
      first_token_at: new Date("2024-01-01T00:00:00.100Z").toISOString(),
    } as const;
    // committed：总耗时 = 请求到达 → 当前 = 1000ms；首字耗时 = 100ms。
    expect(formatElapsedWithFirst(committed, now)).toBe("首字 100ms · 总耗时 1s");
  });
});

describe("<LogsPage /> 追踪 Sheet 诊断区块与时间线", () => {
  /** 推送一条实时请求并经行点击打开追踪 Sheet，返回 dialog 元素 */
  async function openTrace(log: Record<string, unknown>) {
    setupBasicFetch();
    const sources = setupEventSource();
    const { container } = render(<LogsPage />, { wrapper: Wrapper });
    await waitFor(() => {
      const g = container.querySelector('[role="grid"]');
      if (!g) throw new Error("grid not yet");
    });
    await waitFor(() => sources.length > 0 && sources[0]!.readyState !== 0);
    const es = sources[0]!;
    await act(async () => {
      es.fireEvent("log", log);
    });
    const row = await waitFor(() => {
      const el = screen.getByLabelText(`查看请求 #${(log as { id: number }).id} 追踪`);
      if (!el) throw new Error("row not yet");
      return el;
    });
    fireEvent.click(row);
    return (await waitFor(() => {
      const d = document.querySelector('[role="dialog"]');
      if (!d) throw new Error("dialog not yet");
      return d as HTMLElement;
    })) as HTMLElement;
  }

  it("诊断区块：出口代理、协议链路、透传模式、估算标记一眼可读", async () => {
    const dialog = await openTrace({
      ...makeLog(1),
      relay_mode: "passthrough",
      proxy_addr: "http://10.0.0.9:7897",
      usage_estimated: true,
    });
    expect(within(dialog).getByText("成功")).toBeTruthy();
    // success 终态：首字 100ms · 总耗时 110ms（makeLog 首字固定 100ms，duration_ms=110）
    expect(within(dialog).getByText(/首字 100ms · 总耗时 110ms/)).toBeTruthy();
    expect(within(dialog).getByText(/tokens 101 \/ 51/)).toBeTruthy();
    expect(within(dialog).getByText(/（估算）/)).toBeTruthy();
    expect(within(dialog).getByText("出口代理 http://10.0.0.9:7897")).toBeTruthy();
    expect(within(dialog).getByText("openai_chat → openai")).toBeTruthy();
    expect(within(dialog).getByText("协议透传")).toBeTruthy();
    expect(within(dialog).queryByText("直连（未走代理）")).toBeNull();
  });

  it("诊断区块：无代理显示直连，转换模式且无估算标记", async () => {
    const dialog = await openTrace(makeLog(1));
    expect(within(dialog).getByText("直连（未走代理）")).toBeTruthy();
    expect(within(dialog).getByText("协议转换")).toBeTruthy();
    expect(within(dialog).queryByText("（估算）")).toBeNull();
  });

  it("时间线：成功尝试三行布局(渠道+模型+密钥/代理/首字+耗时)，失败尝试保持单行", async () => {
    const dialog = await openTrace({
      ...makeLog(1),
      attempts: [
        {
          seq: 1,
          channel_id: 1,
          channel_name: "openai-prod",
          member_id: 1,
          model: "gpt-4o",
          key_label: "#1(主)",
          proxy_addr: "socks5://***@10.0.0.9:1080",
          first_token_ms: 200,
          latency_ms: 1500,
          outcome: "success",
        },
        {
          seq: 2,
          channel_id: 1,
          channel_name: "openai-prod",
          member_id: 2,
          model: "gpt-4o",
          latency_ms: 0,
          outcome: "failed",
          err_class: "timeout",
          err_brief: "上游超时",
        },
      ],
    });
    const ol = dialog.querySelector("ol");
    expect(ol).toBeTruthy();
    const timeline = within(ol as HTMLElement);
    // 成功尝试第一行：渠道 + 模型 + 密钥标签
    expect(timeline.getAllByText("openai-prod").length).toBeGreaterThanOrEqual(1);
    expect(timeline.getAllByText("gpt-4o").length).toBeGreaterThanOrEqual(1);
    expect(timeline.getByText("#1(主)")).toBeTruthy();
    // 成功尝试第二行：代理详情
    expect(timeline.getByText(/代理 socks5:\/\/\*\*\*@10\.0\.0\.9:1080/)).toBeTruthy();
    // 成功尝试第三行：首字 + 总耗时
    expect(timeline.getByText(/首字 200ms · 总耗时 1500ms/)).toBeTruthy();
    // 失败尝试保持单行：0 显示「—」，失败分类与摘要可见
    expect(timeline.getByText("—")).toBeTruthy();
    expect(timeline.getByText("timeout")).toBeTruthy();
    expect(timeline.getByText("上游超时")).toBeTruthy();
  });

  it("时间线：成功尝试无首字时回退纯总耗时，无代理时显示直连", async () => {
    const dialog = await openTrace({
      ...makeLog(1),
      attempts: [
        {
          seq: 1,
          channel_id: 1,
          channel_name: "direct-ch",
          member_id: 1,
          model: "gpt-4o",
          latency_ms: 800,
          outcome: "success",
        },
      ],
    });
    const ol = dialog.querySelector("ol");
    expect(ol).toBeTruthy();
    const timeline = within(ol as HTMLElement);
    // 无 first_token_ms 时回退纯总耗时
    expect(timeline.getByText(/总耗时 800ms/)).toBeTruthy();
    expect(timeline.queryByText(/首字/)).toBeNull();
    // 无代理时显示直连
    expect(timeline.getByText("直连（未走代理）")).toBeTruthy();
  });
});
