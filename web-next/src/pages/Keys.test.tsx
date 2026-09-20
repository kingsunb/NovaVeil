import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import KeysPage from "./Keys";

const STORAGE_KEY = "nv-auth";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        {children}
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

function mockFetchJson(data: unknown, status = 200) {
  return vi.fn(() =>
    Promise.resolve(
      new Response(JSON.stringify({ code: 200, message: "success", data }), {
        status,
        headers: { "content-type": "application/json" },
      }),
    ),
  );
}

describe("<KeysPage /> 创建密钥后展示「仅此一次」", () => {
  it("创建成功弹出含明文 api_key 的对话框", async () => {
    const user = userEvent.setup();

    // 第一次 list 返空
    let callCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/apikey/list")) {
          callCount++;
          return Promise.resolve(
            new Response(
              JSON.stringify({ code: 200, message: "success", data: [] }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        if (url.includes("/apikey/create")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: {
                  id: 1,
                  name: "k1",
                  api_key: "sk-NEW-1234-ABCD",
                  api_key_masked: "sk-N…1234",
                  enabled: true,
                  expire_at: 0,
                  supported_models: "",
                },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return mockFetchJson(null)();
      }),
    );

    render(<KeysPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /创建密钥/ }));

    // 填名字提交
    const nameInput = screen.getByPlaceholderText(/ci-runner/);
    await user.type(nameInput, "k1");
    await user.click(screen.getByRole("button", { name: /^创建$/ }));

    // 「仅此一次」对话框出现并含明文
    await waitFor(() => {
      expect(screen.getByTestId("created-key")).toHaveTextContent("sk-NEW-1234-ABCD");
    });
    expect(screen.getByText(/出于安全考虑/)).toBeInTheDocument();

    // 关闭后明文消失
    await user.click(screen.getByRole("button", { name: /我已保存/ }));
    await waitFor(() => {
      expect(screen.queryByTestId("created-key")).not.toBeInTheDocument();
    });
  });

  it("支持模型从分组多选，并按逗号格式提交", async () => {
    const user = userEvent.setup();
    let createBody: Record<string, unknown> | null = null;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/apikey/list")) return Promise.resolve(new Response(JSON.stringify({ code: 200, message: "success", data: [] }), { status: 200, headers: { "content-type": "application/json" } }));
        if (url.includes("/group/list")) {
          return Promise.resolve(new Response(JSON.stringify({
            code: 200,
            message: "success",
            data: [
              { id: 1, name: "gpt-4o-prod", mode: "manual", active_item_id: 0, relay_config: {}, items: [] },
              { id: 2, name: "claude-prod", mode: "failover", active_item_id: 0, relay_config: {}, items: [] },
            ],
          }), { status: 200, headers: { "content-type": "application/json" } }));
        }
        if (url.includes("/apikey/create")) {
          createBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return Promise.resolve(new Response(JSON.stringify({
            code: 200,
            message: "success",
            data: {
              id: 8,
              name: "selector",
              api_key: "sk-NEW-SELECTOR",
              api_key_masked: "****CTOR",
              enabled: true,
              expire_at: 0,
              supported_models: "claude-prod",
            },
          }), { status: 200, headers: { "content-type": "application/json" } }));
        }
        return mockFetchJson(null)();
      }),
    );

    render(<KeysPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /创建密钥/ }));
    const option = (name: string) =>
      screen.getAllByRole("button").find((button) => button.textContent === name) ??
      (() => {
        throw new Error(`missing model option ${name}`);
      })();
    await waitFor(() => {
      expect(option("gpt-4o-prod")).toBeInTheDocument();
      expect(option("claude-prod")).toBeInTheDocument();
    });
    await user.click(option("gpt-4o-prod"));
    await user.click(option("claude-prod"));
    await user.click(option("gpt-4o-prod"));
    expect((screen.getByLabelText("手动输入支持模型") as HTMLInputElement).value).toBe("claude-prod");

    await user.type(screen.getByPlaceholderText(/ci-runner/), "selector");
    await user.click(screen.getByRole("button", { name: /^创建$/ }));
    await waitFor(() => expect(createBody).not.toBeNull());
    expect((createBody as unknown as Record<string, unknown>).supported_models).toBe("claude-prod");
  });
});

describe("<KeysPage /> 密钥级限流", () => {
  it("创建时提交最大并发与 RPM；负数输入禁止提交", async () => {
    const user = userEvent.setup();
    let createBody: Record<string, unknown> | null = null;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/apikey/list")) {
          return Promise.resolve(
            new Response(JSON.stringify({ code: 200, message: "success", data: [] }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        }
        if (url.includes("/apikey/create")) {
          createBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { id: 9, name: "limited", api_key: "sk-NEW-LIMIT", api_key_masked: "****IMIT", enabled: true, expire_at: 0, supported_models: "" },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return mockFetchJson(null)();
      }),
    );

    render(<KeysPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /创建密钥/ }));
    await user.type(screen.getByPlaceholderText(/ci-runner/), "limited");
    await user.type(screen.getByLabelText("最大并发"), "3");
    await user.type(screen.getByLabelText("每分钟请求数上限"), "120");

    // 负数被视为非法, 保存按钮禁用
    await user.clear(screen.getByLabelText("最大并发"));
    await user.type(screen.getByLabelText("最大并发"), "-1");
    expect(screen.getByRole("button", { name: /^创建$/ })).toBeDisabled();
    expect(screen.getByText(/非负整数/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("最大并发"));
    await user.type(screen.getByLabelText("最大并发"), "3");
    await user.click(screen.getByRole("button", { name: /^创建$/ }));
    await waitFor(() => expect(createBody).not.toBeNull());
    expect((createBody as unknown as Record<string, unknown>).max_concurrent).toBe(3);
    expect((createBody as unknown as Record<string, unknown>).rate_limit_rpm).toBe(120);
  });
});

describe("<KeysPage /> 搜索按名称与密钥值", () => {
  const keys = [
    { id: 1, name: "ci-runner", api_key_masked: "sk-abc…XYZ", enabled: true, max_concurrent: 0, rate_limit_rpm: 0 },
    { id: 2, name: "prod-key",  api_key_masked: "sk-prod…789", enabled: true, max_concurrent: 0, rate_limit_rpm: 0 },
    { id: 3, name: "staging",   api_key_masked: "sk-test…123", enabled: true, max_concurrent: 0, rate_limit_rpm: 0 },
  ];

  function mockList() {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/apikey/list"))
          return Promise.resolve(
            new Response(JSON.stringify({ code: 200, message: "success", data: keys }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        return mockFetchJson(null)();
      }),
    );
  }

  it("按名称搜索", async () => {
    const user = userEvent.setup();
    mockList();
    render(<KeysPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("ci-runner"));

    await user.type(screen.getByPlaceholderText(/搜索/), "prod");
    await waitFor(() => {
      expect(screen.getByText("prod-key")).toBeInTheDocument();
      expect(screen.queryByText("ci-runner")).not.toBeInTheDocument();
      expect(screen.queryByText("staging")).not.toBeInTheDocument();
    });
  });

  it("按密钥值搜索", async () => {
    const user = userEvent.setup();
    mockList();
    render(<KeysPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("ci-runner"));

    await user.type(screen.getByPlaceholderText(/搜索/), "XYZ");
    await waitFor(() => {
      expect(screen.getByText("ci-runner")).toBeInTheDocument();
      expect(screen.queryByText("prod-key")).not.toBeInTheDocument();
      expect(screen.queryByText("staging")).not.toBeInTheDocument();
    });
  });

  it("按密钥前缀搜索", async () => {
    const user = userEvent.setup();
    mockList();
    render(<KeysPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("ci-runner"));

    await user.type(screen.getByPlaceholderText(/搜索/), "sk-test");
    await waitFor(() => {
      expect(screen.getByText("staging")).toBeInTheDocument();
      expect(screen.queryByText("ci-runner")).not.toBeInTheDocument();
      expect(screen.queryByText("prod-key")).not.toBeInTheDocument();
    });
  });
});

describe("<KeysPage /> 创建时间与最后使用时间", () => {
  it("显示创建时间和最后使用时间", async () => {
    const keys = [
      {
        id: 1,
        name: "active-key",
        api_key_masked: "sk-abc…XYZ",
        enabled: true,
        max_concurrent: 0,
        rate_limit_rpm: 0,
        created_at: 1700000000,
        last_used_at: 1700100000,
      },
      {
        id: 2,
        name: "unused-key",
        api_key_masked: "sk-def…UVW",
        enabled: true,
        max_concurrent: 0,
        rate_limit_rpm: 0,
        created_at: 1700050000,
        last_used_at: 0,
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/apikey/list"))
          return Promise.resolve(
            new Response(JSON.stringify({ code: 200, message: "success", data: keys }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        return mockFetchJson(null)();
      }),
    );
    render(<KeysPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("active-key"));

    // active-key: 有创建时间和最后使用时间
    expect(screen.getByText(new Date(1700000000 * 1000).toLocaleString("zh-CN"))).toBeInTheDocument();
    expect(screen.getByText(new Date(1700100000 * 1000).toLocaleString("zh-CN"))).toBeInTheDocument();

    // unused-key: 从未使用
    expect(screen.getByText("从未使用")).toBeInTheDocument();
  });
});


describe("<KeysPage /> 密钥明文 reveal 不走 React Query", () => {
  it("点眼睛直接 fetch 明文并展示，query cache 不落明文", async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let secretCalls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/apikey/list")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: [
                  {
                    id: 7,
                    name: "k7",
                    api_key_masked: "sk-lo…7ABC",
                    enabled: true,
                    max_concurrent: 0,
                    rate_limit_rpm: 0,
                  },
                ],
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        if (url.includes("/apikey/secret/7")) {
          secretCalls++;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { api_key: "sk-PLAIN-7ABC" },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        return mockFetchJson(null)();
      }),
    );

    render(<KeysPage />, {
      wrapper: ({ children }: { children: React.ReactNode }) => (
        <QueryClientProvider client={qc}>
          <MemoryRouter>{children}</MemoryRouter>
        </QueryClientProvider>
      ),
    });

    await screen.findByText("k7");
    await user.click(screen.getByRole("button", { name: "编辑密钥 k7" }));
    await user.click(screen.getByRole("button", { name: "显示密钥" }));

    await waitFor(() => {
      expect(secretCalls).toBe(1);
      expect(screen.getByLabelText("API 密钥明文")).toHaveValue("sk-PLAIN-7ABC");
    });
    // 明文由组件 state 持有，不进入 React Query cache。
    expect(qc.getQueryData(["apikeys", "secret", 7])).toBeUndefined();

    // 隐藏后立即清空明文；关闭前都不缓存。
    await user.click(screen.getByRole("button", { name: "隐藏密钥" }));
    expect(screen.getByLabelText("API 密钥明文")).toHaveValue("••••••••••••••••");
    expect(qc.getQueryData(["apikeys", "secret", 7])).toBeUndefined();

    vi.unstubAllGlobals();
  });
});
