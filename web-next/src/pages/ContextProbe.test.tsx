import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";

import ContextProbePage from "./ContextProbe";
import {
  MAX_PROBE_TOKENS,
  PROBE_SIZES,
  formatExactTokens,
  formatWindowTokens,
} from "@/lib/context-probe";
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

const channel: Channel = {
  ...sampleChannel,
  id: 7,
  name: "OpenCode Free",
  is_free: true,
  models: [
    { id: 70, channel_id: 7, name: "model-a", source: "auto", context_window: 1_000_000 },
    { id: 71, channel_id: 7, name: "model-b", source: "auto" },
  ],
  model_limits: { "model-b": { context_window: 128_000 } },
};

describe("ContextProbe 标称档位", () => {
  it("统一十进制 token: 1M = 1,000,000", () => {
    expect(MAX_PROBE_TOKENS).toBe(1_000_000);
    expect(PROBE_SIZES.map((size) => size.value)).toEqual([
      64_000, 128_000, 256_000, 500_000, 1_000_000,
    ]);
  });

  it("窗口数值最多三位小数并带精确 token 文案", () => {
    expect(formatWindowTokens(131_072)).toBe("131.072K");
    expect(formatWindowTokens(128_000)).toBe("128K");
    expect(formatWindowTokens(1_000_000)).toBe("1M");
    expect(formatWindowTokens(999)).toBe("999");
    expect(formatExactTokens(131_072)).toBe("131,072 token");
  });
});

describe("<ContextProbePage /> 窗口定义", () => {
  it("勾选多个模型后可按渠道 × 模型批量定义窗口", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          applyBodies.push(JSON.parse(String(init?.body ?? "{}")));
          return jsonResponse(null);
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));

    expect(await screen.findByText("model-a")).toBeInTheDocument();
    expect(screen.getByText("model-b")).toBeInTheDocument();
    // 同步目录值只展示, 人工值才标"已定义"。
    expect(screen.getByText("同步 1M")).toBeInTheDocument();
    expect(screen.getByText("已定义 128K")).toBeInTheDocument();

    await user.click(screen.getByLabelText("选择渠道 OpenCode Free"));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "批量定义（2）" })).toBeInTheDocument(),
    );

    await user.type(screen.getByLabelText("批量窗口 token"), "128000");
    await user.click(screen.getByRole("button", { name: "批量定义（2）" }));

    await waitFor(() => expect(applyBodies).toHaveLength(2));
    expect(applyBodies).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ channel_id: 7, model: "model-a", context_window: 128_000 }),
        expect.objectContaining({ channel_id: 7, model: "model-b", context_window: 128_000 }),
      ]),
    );
  });

  it("保存期间继续编辑时保留新草稿", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    let resolveApply: ((response: Response) => void) | undefined;
    const pendingApply = new Promise<Response>((resolve) => {
      resolveApply = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          applyBodies.push(JSON.parse(String(init?.body ?? "{}")));
          return pendingApply;
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    const input = await screen.findByLabelText("model-b 窗口 token");
    expect(input).toHaveValue(128000);

    await user.click(screen.getByLabelText("保存 model-b 窗口"));
    await waitFor(() => expect(applyBodies).toHaveLength(1));

    await user.clear(input);
    await user.type(input, "256000");
    resolveApply?.(new Response(JSON.stringify({ code: 200, message: "success", data: null }), {
      status: 200,
      headers: { "content-type": "application/json" },
    }));

    await waitFor(() => expect(input).toHaveValue(256000));
  });

  it("空输入不提交，清除只走清除按钮", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          applyBodies.push(JSON.parse(String(init?.body ?? "{}")));
          return jsonResponse(null);
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    const input = await screen.findByLabelText("model-b 窗口 token");
    await user.clear(input);

    const saveButton = screen.getByLabelText("保存 model-b 窗口");
    expect(saveButton).toBeDisabled();
    expect(saveButton).toHaveAttribute(
      "title",
      "输入为空；如需清除已定义窗口请点「清除」",
    );
    expect(applyBodies).toHaveLength(0);

    await user.click(screen.getByLabelText("清除 model-b 窗口"));
    await waitFor(() => expect(applyBodies).toHaveLength(1));
    expect(applyBodies[0]).toEqual({
      channel_id: 7,
      model: "model-b",
      context_window: 0,
    });
  });
});

describe("<ContextProbePage /> 精度与批量语义", () => {
  it("窗口数值最多三位小数, 标记带精确 token tooltip", async () => {
    const precisionChannel: Channel = {
      ...channel,
      model_limits: { "model-b": { context_window: 131_072 } },
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/channel/list")) return jsonResponse([precisionChannel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));

    expect(await screen.findByText("已定义 131.072K")).toBeInTheDocument();
    expect(screen.getByText("同步 1M")).toBeInTheDocument();
    expect(
      screen.getByTitle("人工覆盖的上下文窗口：131,072 token；优先于同步目录值"),
    ).toBeInTheDocument();
    expect(
      screen.getByTitle("同步目录值：1,000,000 token；无人工覆盖时参与路由跳过"),
    ).toBeInTheDocument();
  });

  it("批量清除只清已定义窗口并跳过未定义项", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          applyBodies.push(JSON.parse(String(init?.body ?? "{}")));
          return jsonResponse(null);
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    await user.click(screen.getByLabelText("选择渠道 OpenCode Free"));

    const clearButton = screen.getByRole("button", { name: "批量清除" });
    await waitFor(() => expect(clearButton).toBeEnabled());
    await user.click(clearButton);

    await waitFor(() => expect(applyBodies).toHaveLength(1));
    expect(applyBodies[0]).toEqual({
      channel_id: 7,
      model: "model-b",
      context_window: 0,
    });
  });

  it("仅勾选未定义窗口的模型时批量清除不提交请求", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          applyBodies.push(JSON.parse(String(init?.body ?? "{}")));
          return jsonResponse(null);
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    await user.click(screen.getByLabelText("选择 model-a"));
    await user.click(screen.getByRole("button", { name: "批量清除" }));

    expect(applyBodies).toHaveLength(0);
  });

  it("空选择时批量与探测按钮均不可用", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));

    expect(screen.getByRole("button", { name: "批量定义（0）" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "批量清除" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "开始探测（0 组）" })).toBeDisabled();
  });

  it("批量定义部分失败后按钮恢复且未保存草稿保留", async () => {
    const applyBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/apply-context")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as { model?: string };
          applyBodies.push(body);
          if (body.model === "model-a") {
            return Promise.resolve(
              new Response(JSON.stringify({ code: 500, message: "保存失败", data: null }), {
                status: 500,
                headers: { "content-type": "application/json" },
              }),
            );
          }
          return jsonResponse(null);
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));

    const draftInput = await screen.findByLabelText("model-a 窗口 token");
    await user.type(draftInput, "999999");
    await user.click(screen.getByLabelText("选择渠道 OpenCode Free"));
    await user.type(screen.getByLabelText("批量窗口 token"), "128000");

    const batchButton = screen.getByRole("button", { name: "批量定义（2）" });
    await waitFor(() => expect(batchButton).toBeEnabled());
    await user.click(batchButton);

    await waitFor(() => expect(applyBodies).toHaveLength(2));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "批量定义（2）" })).toBeEnabled(),
    );
    expect(draftInput).toHaveValue(999999);
    expect(screen.getByRole("button", { name: "批量清除" })).toBeEnabled();
  });
});

describe("<ContextProbePage /> 同模型串行调度", () => {
  it("同一模型的多档探测不并发且按档位升序执行", async () => {
    const probeBodies: Array<{ channel_id: number; model: string; tokens: number }> = [];
    let inFlight = 0;
    let peakInFlight = 0;
    const releases: Array<() => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/probe-context")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as {
            channel_id: number;
            model: string;
            tokens: number;
          };
          probeBodies.push(body);
          inFlight += 1;
          peakInFlight = Math.max(peakInFlight, inFlight);
          return new Promise<Response>((resolve) => {
            releases.push(() => {
              inFlight -= 1;
              resolve(
                okResponse({
                  tokens: body.tokens,
                  accepted: true,
                  status: 200,
                  detail: "ok",
                  latency_ms: 5,
                  actual_tokens: body.tokens,
                }),
              );
            });
          });
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    await user.click(screen.getByLabelText("选择 model-b"));
    await user.click(screen.getByText("64K"));
    await user.click(screen.getByRole("button", { name: "开始探测（2 组）" }));

    await waitFor(() => expect(probeBodies).toHaveLength(1));
    expect(probeBodies[0]).toMatchObject({ channel_id: 7, model: "model-b", tokens: 64_000 });
    expect(releases).toHaveLength(1);
    expect(peakInFlight).toBe(1);

    releases.shift()?.();
    await waitFor(() => expect(probeBodies).toHaveLength(2));
    expect(probeBodies[1]).toMatchObject({ model: "model-b", tokens: 128_000 });
    expect(peakInFlight).toBe(1);

    releases.shift()?.();
    await waitFor(() => expect(screen.getAllByText("接受")).toHaveLength(2));
    expect(peakInFlight).toBe(1);
  });

  it("不同模型仍可并行探测", async () => {
    const probeBodies: Array<{ model: string }> = [];
    let inFlight = 0;
    let peakInFlight = 0;
    const releases: Array<() => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/model-eval/probe-context")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as {
            model: string;
            tokens: number;
          };
          probeBodies.push(body);
          inFlight += 1;
          peakInFlight = Math.max(peakInFlight, inFlight);
          return new Promise<Response>((resolve) => {
            releases.push(() => {
              inFlight -= 1;
              resolve(
                okResponse({
                  tokens: body.tokens,
                  accepted: true,
                  status: 200,
                  detail: "ok",
                  latency_ms: 5,
                  actual_tokens: body.tokens,
                }),
              );
            });
          });
        }
        if (url.includes("/channel/list")) return jsonResponse([channel]);
        if (url.includes("/model-eval/probe-history")) return jsonResponse([]);
        return jsonResponse(null);
      }),
    );

    render(
      <Wrapper>
        <ContextProbePage />
      </Wrapper>,
    );

    const user = userEvent.setup();
    await screen.findByText("OpenCode Free");
    await user.click(screen.getByLabelText("展开渠道"));
    await user.click(screen.getByLabelText("选择渠道 OpenCode Free"));
    await user.click(screen.getByRole("button", { name: "开始探测（2 组）" }));

    await waitFor(() => expect(probeBodies).toHaveLength(2));
    expect(peakInFlight).toBe(2);

    while (releases.length > 0) releases.shift()?.();
    await waitFor(() => expect(screen.getAllByText("接受")).toHaveLength(2));
  });
});
