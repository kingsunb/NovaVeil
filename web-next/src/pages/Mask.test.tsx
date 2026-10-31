import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { toast } from "sonner";

import MaskPage from "./Mask";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import type { MaskConfig, MaskRuleMeta } from "@/lib/types";

// mock 掉 toast，便于断言报错行为
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>{children}</MemoryRouter>
      </QueryClientProvider>
    </ThemeProvider>
  );
}

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

const rules: MaskRuleMeta[] = [
  { label: "phone", description: "手机号", default_enabled: true },
  { label: "email", description: "邮箱", default_enabled: false },
];

function maskConfig(over: Partial<MaskConfig> = {}): MaskConfig {
  return {
    enabled: false,
    builtin_rule_switch: {},
    custom_terms: [],
    ...over,
  };
}

interface RecordedCall {
  method: string;
  url: string;
  body?: unknown;
}

/** 脱敏接口 mock：GET 每次读当前快照 */
function setupMaskFetch(getConfig: () => MaskConfig): RecordedCall[] {
  const calls: RecordedCall[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      const body =
        typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ method, url, body });
      if (url.includes("/mask/rules")) {
        return Promise.resolve(jsonOk(rules));
      }
      if (url.includes("/mask/config")) {
        return Promise.resolve(jsonOk(getConfig()));
      }
      return Promise.resolve(jsonOk(null));
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("<MaskPage /> 写前重取", () => {
  it("切开关前先重取最新配置，PUT 合并别处新增的规则开关与拦截词", async () => {
    const user = userEvent.setup();
    let remote = maskConfig();
    const calls = setupMaskFetch(() => remote);
    render(<MaskPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("全局总开关"));

    // 模拟别处（另一标签页/接口）更新了配置
    remote = maskConfig({
      builtin_rule_switch: { phone: true },
      custom_terms: [{ value: "张三", category: "人名" }],
    });

    // 页面第一个 switch 是全局总开关
    await user.click(screen.getAllByRole("switch")[0]);

    await waitFor(() => {
      expect(
        calls.some((c) => c.method === "PUT" && c.url.includes("/mask/config")),
      ).toBe(true);
    });

    const maskCalls = calls.filter((c) => c.url.includes("/mask/config"));
    const putIdx = maskCalls.findIndex((c) => c.method === "PUT");
    // PUT 前必须是写前重取的 GET
    expect(maskCalls[putIdx - 1]?.method).toBe("GET");
    const putBody = maskCalls[putIdx].body as MaskConfig;
    expect(putBody.enabled).toBe(true);
    expect(putBody.builtin_rule_switch).toEqual({ phone: true });
    expect(putBody.custom_terms).toEqual([
      { value: "张三", category: "人名" },
    ]);
  });

  it("写前重取失败：toast 报错且不发 PUT", async () => {
    const user = userEvent.setup();
    let configCalls = 0;
    const calls: RecordedCall[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const method = (init?.method ?? "GET").toUpperCase();
        calls.push({ method, url });
        if (url.includes("/mask/rules")) {
          return Promise.resolve(jsonOk(rules));
        }
        if (url.includes("/mask/config")) {
          configCalls += 1;
          if (configCalls === 1) return Promise.resolve(jsonOk(maskConfig()));
          return Promise.resolve(
            new Response(JSON.stringify({ code: 500, message: "db down" }), {
              status: 500,
              headers: { "content-type": "application/json" },
            }),
          );
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<MaskPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("全局总开关"));

    await user.click(screen.getAllByRole("switch")[0]);

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("db down");
    });
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });
});

describe("<MaskPage /> 删除拦截词", () => {
  it("删除按词匹配新快照，不误删别处新增的条目", async () => {
    const user = userEvent.setup();
    let remote = maskConfig({
      custom_terms: [
        { value: "词甲", category: "" },
        { value: "词乙", category: "" },
      ],
    });
    const calls = setupMaskFetch(() => remote);
    render(<MaskPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("词甲"));

    // 别处新增词丙后旧下标已错位
    remote = maskConfig({
      custom_terms: [
        { value: "词丙", category: "" },
        { value: "词甲", category: "" },
        { value: "词乙", category: "" },
      ],
    });

    await user.click(
      screen.getByRole("button", { name: "删除自定义拦截词 词乙" }),
    );
    await waitFor(() => screen.getByRole("dialog"));
    await user.click(screen.getByRole("button", { name: "删除" }));

    await waitFor(() => {
      expect(calls.some((c) => c.method === "PUT")).toBe(true);
    });
    const putBody = calls.find((c) => c.method === "PUT")!.body as MaskConfig;
    expect(putBody.custom_terms.map((t) => t.value)).toEqual([
      "词丙",
      "词甲",
    ]);
  });
});
