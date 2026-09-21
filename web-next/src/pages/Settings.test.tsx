import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import SettingsPage from "./Settings";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import { AuthProvider } from "@/store/auth";
import { sampleChannel } from "@/test/fixtures/channels";

const STORAGE_KEY = "nv-auth";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        {/* ChangePasswordForm 用了 useAuth，必须包 AuthProvider */}
        <AuthProvider>
          <MemoryRouter>
            {children}
          </MemoryRouter>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );
}

beforeEach(() => {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

/**
 * 回归：Settings.tsx RetentionField 之前有
 *   useState(() => { setV(value) })
 * 的错误模式，导致 query 重新拉取后表单不更新。
 * 修后：用 useEffect 同步 value 到 localDraft。
 */
describe("<SettingsPage /> RetentionField 同步", () => {
  it("query 拉到 5 后，input 显示 5", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("error_retention_days")) {
          return Promise.resolve(jsonOk({ key: "error_retention_days", value: "5" }));
        }
        if (url.includes("error_retention_max_count")) {
          return Promise.resolve(jsonOk({ key: "error_retention_max_count", value: "100" }));
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<SettingsPage />, { wrapper: Wrapper });
    await userEvent.click(screen.getByRole("button", { name: "错误日志保留" }));
    await waitFor(() => {
      // happy-dom 的 getByLabelText 无法排除 <label> 内按钮文本，
      // 改用 text → closest label → querySelector 定位 input
      const labelSpan = screen.getByText("错误保留天数");
      const input = labelSpan.closest("label")!.querySelector("input") as HTMLInputElement;
      expect(input.value).toBe("5");
    });
  });
});

describe("<SettingsPage /> TesterSection 分组模型测试", () => {
  const sampleGroup = {
    id: 1,
    name: "default",
    mode: "random",
    active_item_id: 0,
    display_order: 0,
    relay_config: {
      retry_count: 1,
      timeout: 0,
      fallback_on_error: false,
    },
    items: [
      { channel_model_id: 100, ref_group_name: "", priority: 0 },
      { channel_model_id: 101, ref_group_name: "", priority: 1 },
    ],
  };

  function setupGroupFetch() {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/group/list")) {
        return Promise.resolve(jsonOk([sampleGroup]));
      }
      if (url.includes("/group/test") && method === "POST") {
        return Promise.resolve(
          jsonOk([
            { channel_name: "openai-prod", model: "gpt-4o", status: "ok", content: "pong", latency_ms: 230 },
            { channel_name: "openai-prod", model: "gpt-4o-mini", status: "fail", error: "model not supported", latency_ms: 0 },
          ]),
        );
      }
      if (url.includes("/setting/get")) {
        return Promise.resolve(jsonOk({ key: "channel_test_message", value: "" }));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("选分组 → 开始测试 → 表格列出每个成员的测试结果", async () => {
    const user = userEvent.setup();
    const fetchMock = setupGroupFetch();
    render(<SettingsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "模型测试" }));

    // 等分组列表渲染进 select
    await waitFor(() =>
      expect(screen.getByText(/default/)).toBeInTheDocument(),
    );
    await user.selectOptions(
      screen.getByLabelText("分组"),
      String(sampleGroup.id),
    );

    // 点开始测试
    await user.click(screen.getByRole("button", { name: /开始测试/ }));

    // 结果表格应显示每个成员的渠道名和模型名
    await waitFor(() =>
      expect(screen.getByText("gpt-4o")).toBeInTheDocument(),
    );
    await waitFor(() =>
      expect(screen.getByText("gpt-4o-mini")).toBeInTheDocument(),
    );
    await waitFor(() =>
      expect(screen.getAllByText("openai-prod").length).toBeGreaterThanOrEqual(1),
    );

    // 完成 · 1 / 2 通过
    await waitFor(() => {
      expect(screen.getByText(/完成 · 1 \/ 2 通过/)).toBeInTheDocument();
    });

    // 只发了一次 /group/test
    const testCalls = fetchMock.mock.calls.filter((c) =>
      String(c[0]).includes("/group/test"),
    );
    expect(testCalls).toHaveLength(1);
  });

  it("分组无成员：黄色提示", async () => {
    const user = userEvent.setup();
    const emptyGroup = { ...sampleGroup, items: [] };
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/group/list")) {
          return Promise.resolve(jsonOk([emptyGroup]));
        }
        if (url.includes("/group/test") && method === "POST") {
          return Promise.resolve(jsonOk([]));
        }
        if (url.includes("/setting/get")) {
          return Promise.resolve(jsonOk({ key: "channel_test_message", value: "" }));
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<SettingsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "模型测试" }));
    await waitFor(() =>
      expect(screen.getByText(/default/)).toBeInTheDocument(),
    );
    await user.selectOptions(
      screen.getByLabelText("分组"),
      String(emptyGroup.id),
    );
    await user.click(screen.getByRole("button", { name: /开始测试/ }));
    await waitFor(() =>
      expect(
        screen.getByText(/该分组没有成员/),
      ).toBeInTheDocument(),
    );
  });
});

/**
 * STA-16: 导入成功后必须失效 channels / groups / keys 等全部受影响查询缓存，
 * 否则用户导入后立即浏览各分区仍看到旧值，基于旧值编辑会把导入覆盖回去。
 */
describe("<SettingsPage /> BackupSection 导入后失效受影响查询", () => {
  it("导入后 channels / groups / keys 等全部分区查询被标记为 invalidated", async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    // 预置各分区缓存（模拟导入前用户已浏览过这些页面）。
    qc.setQueryData(["channels"], [sampleChannel]);
    qc.setQueryData(["groups"], [{ id: 1, name: "g1" }]);
    qc.setQueryData(["keys"], [{ id: 1, name: "k1" }]);
    qc.setQueryData(["apikeys", "secret", 1], { api_key: "sk-old" });
    qc.setQueryData(["mask-config"], { masks: [] });
    qc.setQueryData(["mask-rules"], { rules: [] });
    qc.setQueryData(["usage-heatmap", 365], []);
    qc.setQueryData(["token-trends", "30d"], []);
    qc.setQueryData(["recent-errors", 5], []);
    qc.setQueryData(["log-errors", "all"], []);
    qc.setQueryData(["log-stop-all-state"], { stopping: false });
    qc.setQueryData(["model-eval", "queue", "list"], []);
    expect(qc.getQueryState(["channels"])?.isInvalidated).toBe(false);

    function Wrapper({ children }: { children: React.ReactNode }) {
      return (
        <ThemeProvider>
          <QueryClientProvider client={qc}>
            <AuthProvider>
              <MemoryRouter>
                {children}
              </MemoryRouter>
            </AuthProvider>
          </QueryClientProvider>
        </ThemeProvider>
      );
    }

    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/setting/import") && method === "POST") {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                code: 200,
                message: "success",
                data: { rows_affected: { channels: 1, groups: 1, api_keys: 1, settings: 2 } },
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            ),
          );
        }
        // 其它端点返回空信封，避免启动探活等干扰。
        return Promise.resolve(jsonOk(null));
      }),
    );

    render(<SettingsPage />, { wrapper: Wrapper });

    // 切到「备份」分区并上传合法 DBDump。
    await user.click(screen.getByRole("button", { name: "备份" }));
    const fileInput = screen.getByLabelText("选择要导入的 JSON 文件");
    const dump = new File(
      [JSON.stringify({ version: 1, channels: [{ key: "sk-IMPORT-PLAINTEXT" }], groups: [], api_keys: [], settings: [] })],
      "backup.json",
      { type: "application/json" },
    );
    await user.upload(fileInput, dump);

    // 选文件只进入确认，不立刻导入。确认文案写明覆盖范围，交互与清空归档的 ConfirmButton 相同。
    expect(screen.getByText(/覆盖现有渠道、分组、密钥和设置/)).toBeInTheDocument();
    const importCalls = () =>
      (fetch as unknown as { mock: { calls: Array<[string, RequestInit?]> } }).mock.calls.filter(
        ([url, init]) =>
          String(url).includes("/setting/import") &&
          (init?.method ?? "GET").toUpperCase() === "POST",
      );
    expect(importCalls()).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "确认导入" }));
    expect(importCalls()).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "再次点击确认" }));

    // 导入完成后各分区查询应被 invalidated（浏览时立即重新拉取）。
    await waitFor(() => {
      expect(screen.getByTestId("import-summary")).toBeInTheDocument();
    });
    const held = qc.getMutationCache().getAll().some((mutation) => {
      const blob = JSON.stringify({
        data: mutation.state.data,
        variables: mutation.state.variables,
      });
      return blob.includes("sk-IMPORT-PLAINTEXT");
    });
    expect(held).toBe(false);
    expect(qc.getQueryState(["channels"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["groups"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["keys"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["apikeys", "secret", 1])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["mask-config"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["mask-rules"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["usage-heatmap", 365])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["token-trends", "30d"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["recent-errors", 5])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["log-errors", "all"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["log-stop-all-state"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["model-eval", "queue", "list"])?.isInvalidated).toBe(true);

    vi.unstubAllGlobals();
  });
});

describe("<SettingsPage /> 调用客户端统计", () => {
  it("显示全部条目而非截断 20 条", async () => {
    const user = userEvent.setup();
    // 生成 25 条客户端统计
    const stats = Array.from({ length: 25 }, (_, i) => ({
      client_ip: `10.0.0.${i + 1}`,
      requests: 100 - i,
      first_seen: "2024-01-01T00:00:00Z",
      last_seen: "2024-01-15T00:00:00Z",
    }));
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/log/client-stats")) return Promise.resolve(jsonOk(stats));
        if (url.includes("client_stat_max_count"))
          return Promise.resolve(jsonOk({ key: "client_stat_max_count", value: "10000" }));
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<SettingsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "关于" }));

    // 25 个 IP 全部显示（之前截断为 20）
    await waitFor(() => {
      expect(screen.getByText("10.0.0.1")).toBeInTheDocument();
    });
    expect(screen.getByText("10.0.0.25")).toBeInTheDocument();
    expect(screen.getByText(/共 25 个 IP/)).toBeInTheDocument();
  });

  it("保留条数设置控件存在且可保存", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.includes("/log/client-stats")) return Promise.resolve(jsonOk([]));
      if (url.includes("/setting/get") && url.includes("client_stat_max_count"))
        return Promise.resolve(jsonOk({ key: "client_stat_max_count", value: "5000" }));
      if (url.includes("/setting/set") && init?.body) {
        const body = JSON.parse(init.body as string);
        return Promise.resolve(jsonOk({ key: body.key, value: body.value }));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<SettingsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "关于" }));

    // 设置控件存在
    const input = await screen.findByLabelText("客户端统计最大保留条数") as HTMLInputElement;
    expect(input.value).toBe("5000");

    // 修改并保存
    await user.clear(input);
    await user.type(input, "8000");
    await user.click(screen.getByRole("button", { name: "保存 客户端统计保留条数" }));
    await waitFor(() => {
      const setCalls = fetchMock.mock.calls.filter(([u, init]) =>
        String(u).includes("/setting/set") && init?.body,
      );
      expect(setCalls.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(setCalls[setCalls.length - 1][1]?.body as string);
      expect(body.key).toBe("client_stat_max_count");
      expect(body.value).toBe("8000");
    });
  });
});

describe("<SettingsPage /> SyncSection 自动同步间隔", () => {
  it("显示后端同步间隔并支持保存", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.includes("/channel/last-sync-time")) {
        return Promise.resolve(jsonOk({ last_sync_at: "2026-09-19T00:00:00Z" }));
      }
      if (url.includes("/setting/get") && url.includes("sync_llm_interval")) {
        return Promise.resolve(jsonOk({ key: "sync_llm_interval", value: "12" }));
      }
      if (url.includes("/setting/set") && init?.body) {
        const body = JSON.parse(init.body as string);
        return Promise.resolve(jsonOk({ key: body.key, value: body.value }));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<SettingsPage />, { wrapper: Wrapper });

    await user.click(screen.getByRole("button", { name: "上游模型同步" }));
    const input = (await screen.findByLabelText("自动同步间隔")) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("12"));

    await user.clear(input);
    await user.type(input, "6");
    await user.click(screen.getByRole("button", { name: "保存 自动同步间隔" }));

    await waitFor(() => {
      const setCalls = fetchMock.mock.calls.filter(([u, init]) =>
        String(u).includes("/setting/set") && init?.body,
      );
      expect(setCalls.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(setCalls[setCalls.length - 1][1]?.body as string);
      expect(body.key).toBe("sync_llm_interval");
      expect(body.value).toBe("6");
    });
  });
});
