import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";

import ContextProbePage from "./ContextProbe";
import { MAX_PROBE_TOKENS, PROBE_SIZES } from "@/lib/context-probe";
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

function jsonResponse(data: unknown) {
  return Promise.resolve(
    new Response(JSON.stringify({ code: 200, message: "success", data }), {
      status: 200,
      headers: { "content-type": "application/json" },
    }),
  );
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

    expect(screen.getByLabelText("保存 model-b 窗口")).toBeDisabled();
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
