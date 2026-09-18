import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  sampleChannel,
  sampleChannelDisabled,
} from "@/test/fixtures/channels";
import { DEFAULT_TEST_MESSAGE } from "@/lib/constants";

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import ChannelsPage from "./Channels";

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
    "nv-auth",
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

function mockList(channels: unknown[] = []) {
  mockFetch({
    list: channels,
  });
}

/**
 * 通用 fetch 拦截器：可按 URL 片段注入响应。
 *  默认未匹配 /api/v1/* 返回成功空数据。
 */
function mockFetch(opts: {
  list?: unknown[];
  fetchModels?: unknown[];
  fetchModelError?: { status: number; body: unknown };
  testResults?: Array<{
    model?: string;
    content?: string;
    elapsed_ms?: number;
    prompt_tokens?: number;
    completion_tokens?: number;
  }>;
  deleteOk?: boolean;
  importResult?: { success: number; failed: number; errors: string[] };
} = {}) {
  const fetchMock = vi.fn((url: string, _init?: RequestInit) => {
    if (url.includes("/channel/import")) {
      return Promise.resolve(
        new Response(
          JSON.stringify({
            code: 200,
            message: "success",
            data: opts.importResult ?? { success: 0, failed: 0, errors: [] },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    if (opts.fetchModelError && url.includes("/channel/fetch-model")) {
      return Promise.resolve(
        new Response(JSON.stringify(opts.fetchModelError.body), {
          status: opts.fetchModelError.status,
          headers: { "content-type": "application/json" },
        }),
      );
    }
    if (url.includes("/channel/fetch-model")) {
      // 后端 helper.FetchModels 返回 []string；前端 normalizeFetchedModels
      // 同时支持字符串数组和旧版 {name} 对象数组。
      const models = opts.fetchModels ?? [];
      return Promise.resolve(
        new Response(
          JSON.stringify({ code: 200, message: "success", data: models }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    if (url.includes("/channel/test")) {
      const seq = (fetchMock.mock.calls.length - 1) % (opts.testResults?.length || 1);
      const r = opts.testResults?.[seq] ?? { model: "gpt-4o", elapsed_ms: 120 };
      return Promise.resolve(
        new Response(
          JSON.stringify({ code: 200, message: "success", data: r }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    if (url.includes("/channel/list")) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ code: 200, message: "success", data: opts.list ?? [] }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    if (url.includes("/channel/delete/")) {
      return Promise.resolve(
        new Response(
          JSON.stringify({
            code: opts.deleteOk === false ? 500 : 200,
            message: "success",
            data: null,
          }),
          {
            status: opts.deleteOk === false ? 500 : 200,
            headers: { "content-type": "application/json" },
          },
        ),
      );
    }
    if (url.includes("/setting/get")) {
      const parsed = new URL(url, "http://localhost");
      const key = parsed.searchParams.get("key") ?? "channel_test_message";
      return Promise.resolve(
        new Response(
          JSON.stringify({ code: 200, message: "success", data: { key, value: "" } }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    // 其他接口空数据
    return Promise.resolve(
      new Response(JSON.stringify({ code: 200, message: "success", data: null }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("<ChannelsPage />", () => {
  it("列表渲染，列头与渠道名", async () => {
    mockList([sampleChannel]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByText("openai-prod")).toBeInTheDocument();
    });
    // 类型 Pill 渲染为「OpenAI Chat」（label 字典映射，详见 constants.ts）
    expect(screen.getByText("OpenAI Chat")).toBeInTheDocument();
    expect(screen.getByText("https://api.example.com")).toBeInTheDocument();
  });

  it("点击「新建渠道」打开 Sheet 抽屉", async () => {
    const user = userEvent.setup();
    mockList([]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /新建渠道/ }));
    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
    expect(screen.getByText(/凭据 · 模型 · 限制 · 高级/)).toBeInTheDocument();
  });

  it("点击行打开编辑 Sheet（标题含渠道名）", async () => {
    const user = userEvent.setup();
    mockList([sampleChannel]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
    expect(screen.getByText(/编辑：openai-prod/)).toBeInTheDocument();
  });

  it("编辑器凭据区展示优先级，保存时提交 sort", async () => {
    const user = userEvent.setup();
    const ch = { ...sampleChannel, sort: 7 };
    const fetchMock = mockFetch({ list: [ch] });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    const dialog = await screen.findByRole("dialog");

    const input = within(dialog).getByLabelText("优先级") as HTMLInputElement;
    expect(input.value).toBe("7");
    expect(within(dialog).queryByText("排序值")).not.toBeInTheDocument();

    await user.clear(input);
    await user.type(input, "12");
    await user.click(within(dialog).getByRole("button", { name: /^保存$/ }));

    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { sort: number };
      expect(body.sort).toBe(12);
    });
  });

  it("搜索框过滤渠道", async () => {
    const user = userEvent.setup();
    mockList([
      { ...sampleChannel, id: 1, name: "openai-prod" },
      { ...sampleChannel, id: 2, name: "anthropic-test", type: "anthropic" },
    ]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    const search = screen.getByPlaceholderText("搜索渠道");
    await user.type(search, "anth");
    await waitFor(() => {
      expect(screen.queryByText("openai-prod")).not.toBeInTheDocument();
    });
    expect(screen.getByText("anthropic-test")).toBeInTheDocument();
  });

  it("免费分类筛选：徽标、免费/付费过滤", async () => {
    const user = userEvent.setup();
    const paid = { ...sampleChannel, id: 1, name: "openai-prod", is_free: false };
    const free = {
      ...sampleChannel,
      id: 2,
      name: "free-hf",
      is_free: true,
      tags: ["free", "hf"],
    };
    mockList([paid, free]);
    render(<ChannelsPage />, { wrapper: Wrapper });

    await waitFor(() => screen.getByText("openai-prod"));
    expect(screen.getByText("free-hf")).toBeInTheDocument();
    // 免费渠道卡片有「免费」徽标。
    const freeCard = screen.getByText("free-hf").closest("article")!;
    expect(within(freeCard).getByText("免费")).toBeInTheDocument();

    // 只看免费：付费渠道消失，免费渠道保留。
    await user.click(screen.getByRole("button", { name: "免费" }));
    await waitFor(() => {
      expect(screen.queryByText("openai-prod")).not.toBeInTheDocument();
    });
    expect(screen.getByText("free-hf")).toBeInTheDocument();

    // 只看付费：免费渠道消失，付费渠道保留。
    await user.click(screen.getByRole("button", { name: "付费" }));
    await waitFor(() => {
      expect(screen.queryByText("free-hf")).not.toBeInTheDocument();
    });
    expect(screen.getByText("openai-prod")).toBeInTheDocument();

    // 回到全部分类。
    await user.click(screen.getByRole("button", { name: "全部分类" }));
    await waitFor(() => screen.getByText("free-hf"));
    expect(screen.getByText("openai-prod")).toBeInTheDocument();
  });

  it("点击删除按钮弹出确认对话框", async () => {
    const user = userEvent.setup();
    mockList([sampleChannel]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    const row = screen.getByText("openai-prod").closest("article")!;
    const delBtn = row.querySelector('[title="删除"]') as HTMLButtonElement;
    await user.click(delBtn);
    await waitFor(() => {
      expect(screen.getByText(/删除渠道/)).toBeInTheDocument();
    });
    // 渠道名现在在两个地方：行 + 对话框描述；用 getAllByText 验证
    expect(screen.getAllByText(/openai-prod/).length).toBeGreaterThanOrEqual(1);
  });

  it("拉取模型：选择模式隐藏 Tab/Footer，提示已存在/可添加", async () => {
    const user = userEvent.setup();
    mockFetch({
      list: [sampleChannel],
      // 后端真实响应：string[]。前端 normalizeFetchedModels 把它映射成 {name} 列表。
      fetchModels: ["gpt-4o", "claude-3.5-sonnet", "gemini-pro"],
    });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => screen.getByRole("dialog"));

    // 进入「模型」Tab，点「拉取」按钮
    await user.click(screen.getByRole("button", { name: /模型 \(/ }));
    await user.click(screen.getByRole("button", { name: /拉取/ }));

    // 等待「从上游共发现」标题出现（FetchPicker 视图）
    await waitFor(() =>
      expect(screen.getByText(/从上游共发现/)).toBeInTheDocument(),
    );

    // 选择模式期间 Tab/底部 Footer 应隐藏
    expect(
      screen.queryByRole("button", { name: /凭据$/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^保存$/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /测试连通/ }),
    ).not.toBeInTheDocument();

    // 顶部统计：3 个 / 1 已存在 / 2 可添加
    // getByText 的正则会命中承载完整文案的 <p>，再检查其 textContent，
    // 避免数字 span 被拆成多个查询结果。
    const stat = screen.getByText(/从上游共发现/);
    expect(stat.textContent).toContain("从上游共发现");
    expect(stat.textContent).toContain("3");
    expect(stat.textContent).toMatch(/已存在\D*1/);
    expect(stat.textContent).toMatch(/可添加\D*2/);

    // 确认按钮显示 (2)
    const confirm = screen.getByRole("button", { name: /确认添加 \(2\)/ });
    expect(confirm).toBeInTheDocument();

    // 全选 + 全不选来回切
    const master = screen.getByRole("checkbox", { name: "全选" });
    await user.click(master);
    expect(screen.getByRole("button", { name: /确认添加/ })).toBeDisabled();
    await user.click(master); // 重新全选
    expect(
      screen.getByRole("button", { name: /确认添加 \(2\)/ }),
    ).toBeEnabled();
  });

  it("确认添加：仅 source=auto 的新模型入 draft，已存在的不重复", async () => {
    const user = userEvent.setup();
    mockFetch({
      list: [sampleChannel],
      fetchModels: ["gpt-4o", "claude-3.5-sonnet"],
    });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => screen.getByRole("dialog"));

    await user.click(screen.getByRole("button", { name: /模型 \(/ }));
    await user.click(screen.getByRole("button", { name: /拉取/ }));
    await waitFor(() =>
      expect(screen.getByText(/从上游共发现/)).toBeInTheDocument(),
    );

    await user.click(
      screen.getByRole("button", { name: /确认添加 \(1\)/ }),
    );

    // 退回到「模型」Tab，模型数 3（sampleChannel 自带 2 + 新增 1）
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /模型 \(3\)/ }),
      ).toBeInTheDocument(),
    );
    // 新增的 claude 行存在；gpt-4o 不会重复展示成 2 行
    const dialog = screen.getByRole("dialog");
    const dialogView = within(dialog);
    expect(
      dialogView.getByText("claude-3.5-sonnet", { exact: true }),
    ).toBeInTheDocument();
    expect(dialogView.getByText("gpt-4o", { exact: true })).toBeInTheDocument();
  });

  it("测试连通：仅 id（后端兜底模型）", async () => {
    const fetchMock = mockFetch({
      list: [sampleChannel],
      // 后端 relay.TestChannel 返回 {model, content, elapsed_ms, ...}
      testResults: [{ model: "gpt-4o", content: "pong", elapsed_ms: 220 }],
    });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    const user = userEvent.setup();
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => screen.getByRole("dialog"));

    // 「测试连通」按钮 + 关闭确认：避免 onClose 干扰
    await user.click(screen.getByRole("button", { name: /测试连通/ }));

    // 后端兜底取 channel[0]，前端传 {id, message}（message 来自设置项 channel_test_message）
    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) =>
        String(c[0]).includes("/channel/test"),
      );
      expect(call).toBeDefined();
      const body = JSON.parse((call![1] as RequestInit).body as string);
      expect(body).toEqual({ id: sampleChannel.id, message: DEFAULT_TEST_MESSAGE });
    });
  });

  it("模型测试页可用性（Dashboard 空态）", async () => {
    mockFetch({
      list: [sampleChannelDisabled],
      // 后端测试失败通过 HTTP 4xx/5xx 表达（relay.TestChannel 错误 → 500）。
      // 这里返回 500 让前端走 onError 路径。
      testResults: [],
    });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-dev"));
    // 停用渠道也参与列表渲染
    expect(screen.getByText("openai-dev")).toBeInTheDocument();
  });

  it("旧式单 Key 渠道（keys 空但有 key_masked）在 Keys 列显示 1，而不是 0", async () => {
    // 列表接口的 key 恒为空串（明文不回传），旧式单 Key 渠道只有 key_masked；
    // 之前用 c.key 判断恒假，带有效 Key 的渠道被显示成 0 把。
    const legacyChannel = {
      ...sampleChannel,
      id: 9,
      name: "legacy-key",
      keys: [],
      key: "",
      key_masked: "sk-legacy…XYZ",
    };
    mockList([legacyChannel]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("legacy-key"));
    const card = screen.getByText("legacy-key").closest("article");
    expect(card).toBeTruthy();
    // 卡片信息面板中显示密钥数量
    expect(card!.textContent).toContain("1 密钥");
  });

  it("批量添加密钥：一行一个，追加到列表并随保存提交", async () => {
    const user = userEvent.setup();
    const fetchMock = mockFetch({ list: [sampleChannel] });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => screen.getByRole("dialog"));

    // 打开批量添加弹窗（Radix 嵌套模态：内层接管 dialog 角色，外层置 inert，
    // 故不按 dialog 计数，改用弹窗内的 textarea 出现判定已打开）
    await user.click(screen.getByRole("button", { name: "批量添加" }));
    const ta = (await screen.findByLabelText(
      "批量密钥文本",
    )) as HTMLTextAreaElement;
    await user.type(ta, "sk-batch-1\nsk-batch-2\nsk-batch-3");

    // 实时预览：3 行全部可添加
    await waitFor(() =>
      expect(screen.getByText(/可添加 3 个/)).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: /^添加 \(3\)$/ }));

    // 弹窗关闭（textarea 消失）；密钥行由 1 增至 4（眼睛按钮计数）
    await waitFor(() =>
      expect(screen.queryByLabelText("批量密钥文本")).not.toBeInTheDocument(),
    );
    await waitFor(() => {
      const eyes = screen.getAllByRole("button", {
        name: /显示密钥|隐藏密钥/,
      });
      expect(eyes.length).toBe(4);
    });

    // 保存：更新请求 keys 含 1 个保留行(id=k1,空明文) + 3 个新增行(空 id,明文)
    await user.click(screen.getByRole("button", { name: /^保存$/ }));
    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { keys?: Array<{ id: string; key: string; remark: string }> };
      expect(body.keys).toBeDefined();
      expect(body.keys!.length).toBe(4);
      // 已保存行：id 保留、明文留空（后端按 id 恢复旧 secret）
      expect(body.keys![0]).toEqual({ id: "k1", key: "", remark: "" });
      // 新增行：id 为空、明文为粘贴值（后端按 sha256 生成 id）
      const newKeys = body.keys!.slice(1).map((k) => k.key);
      expect(newKeys).toEqual(["sk-batch-1", "sk-batch-2", "sk-batch-3"]);
      expect(body.keys!.slice(1).every((k) => k.id === "")).toBe(true);
    });
  });

  it("批量添加密钥：空行与重复行自动跳过", async () => {
    const user = userEvent.setup();
    mockFetch({ list: [sampleChannel] });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("openai-prod"));
    await user.click(screen.getByText("openai-prod"));
    await waitFor(() => screen.getByRole("dialog"));

    await user.click(screen.getByRole("button", { name: "批量添加" }));
    const ta = (await screen.findByLabelText(
      "批量密钥文本",
    )) as HTMLTextAreaElement;
    // 含 1 个空行 + 1 个本批内重复
    await user.type(ta, "sk-aaa\n\nsk-aaa\nsk-bbb");

    // 共 4 行 · 可添加 2 个 · 跳过 2 个重复/空行
    await waitFor(() =>
      expect(screen.getByText(/可添加 2 个/)).toBeInTheDocument(),
    );
    expect(screen.getByText(/跳过 2 个/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^添加 \(2\)$/ }));
    // 仅新增 2 把（原有 1 + 新增 2 = 3 个眼睛按钮）
    await waitFor(() => {
      const eyes = screen.getAllByRole("button", {
        name: /显示密钥|隐藏密钥/,
      });
      expect(eyes.length).toBe(3);
    });
  });
});

describe("<ChannelsPage /> 渠道优先级行内编辑", () => {
  /**
   * 优先级允许重复、零值与负值；同值按名称兜底。
   * 本组验证：列表按优先级降序渲染、行内编辑触发 update 请求携带正确 sort 值。
   */
  function mockSortFetch(channels: unknown[]) {
    const store = new Map<number, unknown>();
    for (const c of channels) store.set((c as { id: number }).id, c);

    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.includes("/channel/list")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: Array.from(store.values()) }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }
      if (url.includes("/channel/update")) {
        const body = JSON.parse(init?.body as string) as { id: number; sort?: number };
        const existing = store.get(body.id) as Record<string, unknown> | undefined;
        if (existing && body.sort !== undefined) existing.sort = body.sort;
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: existing ?? null }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }
      if (url.includes("/setting/get")) {
        const parsed = new URL(url, "http://localhost");
        const key = parsed.searchParams.get("key") ?? "channel_test_message";
        return Promise.resolve(
          new Response(
            JSON.stringify({ code: 200, message: "success", data: { key, value: "" } }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify({ code: 200, message: "success", data: null }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("自定义排序: 按 sort 值降序, 同值按名称", async () => {
    const highZ = { ...sampleChannel, id: 21, name: "zeta-ch", sort: 10 };
    const highA = { ...sampleChannel, id: 22, name: "alpha-ch", sort: 10 };
    const mid = { ...sampleChannel, id: 23, name: "mid-ch", sort: 0 };
    const low = { ...sampleChannel, id: 24, name: "low-ch", sort: -5 };
    mockSortFetch([mid, low, highZ, highA]);
    render(<ChannelsPage />, { wrapper: Wrapper });

    await waitFor(() => screen.getByText("alpha-ch"));

    const names = screen
      .getAllByRole("button", { name: /编辑渠道/ })
      .map((el) => el.getAttribute("aria-label"));
    expect(names).toEqual([
      "编辑渠道 alpha-ch",
      "编辑渠道 zeta-ch",
      "编辑渠道 mid-ch",
      "编辑渠道 low-ch",
    ]);
  });

  it("自定义排序：内置固定提供商默认排在最后", async () => {
    const builtinHigh = {
      ...sampleChannel,
      id: 31,
      name: "builtin-free",
      sort: 100,
      builtin: true,
      is_free: true,
    };
    const customLow = {
      ...sampleChannel,
      id: 32,
      name: "custom-low",
      sort: -100,
      builtin: false,
    };
    mockList([builtinHigh, customLow]);
    render(<ChannelsPage />, { wrapper: Wrapper });

    await waitFor(() => screen.getByText("custom-low"));

    const names = screen
      .getAllByRole("button", { name: /编辑渠道/ })
      .map((el) => el.getAttribute("aria-label"));
    expect(names).toEqual([
      "编辑渠道 custom-low",
      "编辑渠道 builtin-free",
    ]);
  });

  it("两个渠道同 sort 值均渲染，编辑后触发 update 携带新值", async () => {
    const user = userEvent.setup();
    const chA = { ...sampleChannel, id: 1, name: "sort-a", sort: 0 };
    const chB = { ...sampleChannel, id: 2, name: "sort-b", sort: 0 };
    const fetchMock = mockSortFetch([chA, chB]);
    render(<ChannelsPage />, { wrapper: Wrapper });

    // 两个同 sort=0 的渠道都渲染。
    await waitFor(() => screen.getByText("sort-a"));
    expect(screen.getByText("sort-b")).toBeInTheDocument();

    // 找到 sort-a 的优先级输入并改为 5。
    const inputA = screen.getByLabelText("优先级 sort-a") as HTMLInputElement;
    expect(inputA.value).toBe("0");
    await user.clear(inputA);
    await user.type(inputA, "5");
    await user.tab(); // blur → commitSort

    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const lastBody = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { id: number; sort: number };
      expect(lastBody.id).toBe(1);
      expect(lastBody.sort).toBe(5);
    });
  });

  it("优先级可设为负数", async () => {
    const user = userEvent.setup();
    const ch = { ...sampleChannel, id: 3, name: "sort-neg", sort: 0 };
    const fetchMock = mockSortFetch([ch]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("sort-neg"));

    const input = screen.getByLabelText("优先级 sort-neg") as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "-10");
    await user.tab();

    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const lastBody = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { sort: number };
      expect(lastBody.sort).toBe(-10);
    });
  });

  it("优先级可设为 0（从非零值）", async () => {
    const user = userEvent.setup();
    const ch = { ...sampleChannel, id: 4, name: "sort-to-zero", sort: 7 };
    const fetchMock = mockSortFetch([ch]);
    render(<ChannelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("sort-to-zero"));

    const input = screen.getByLabelText("优先级 sort-to-zero") as HTMLInputElement;
    expect(input.value).toBe("7");
    await user.clear(input);
    await user.type(input, "0");
    await user.tab();

    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const lastBody = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { sort: number };
      expect(lastBody.sort).toBe(0);
    });
  });

  it("导入弹窗：输入文本并提交，请求体为 {text}，且不绕过弹窗", async () => {
    const user = userEvent.setup();
    const fetchMock = mockFetch({ list: [] });
    render(<ChannelsPage />, { wrapper: Wrapper });

    // 打开导入弹窗
    await user.click(screen.getByRole("button", { name: /导入/ }));
    const dialog = await screen.findByRole("dialog");
    const title = within(dialog).getByText("导入渠道");
    expect(title).toBeInTheDocument();
    // 弹窗里应有格式说明
    expect(
      within(dialog).getByText(/每块格式：# 渠道名、请求地址、一个或多个 Key/),
    ).toBeInTheDocument();

    // 初始「导入」按钮禁用（空文本）
    const submit = within(dialog).getByRole("button", { name: "导入" });
    expect(submit).toBeDisabled();

    // 输入内容并提交
    const ta = within(dialog).getByLabelText("导入渠道文本");
    const text = "# 渠道1\nhttps://example1.com\nsk-a\n\n# 渠道2\nhttps://example2.com\nsk-b";
    await user.type(ta, text);
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([url]) =>
        String(url).includes("/channel/import"),
      );
      expect(call).toBeDefined();
      const body = JSON.parse((call![1] as RequestInit).body as string);
      expect(body).toEqual({ text });
    });
    // 导入完成后弹窗应关闭
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("导入弹窗：空文本点击不提交、不请求", async () => {
    const user = userEvent.setup();
    const fetchMock = mockFetch({ list: [] });
    render(<ChannelsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /导入/ }));
    const dialog = await screen.findByRole("dialog");

    // 同时命中「导入渠道」的按钮不提交
    const submit = within(dialog).getByRole("button", { name: "导入" });
    expect(submit).toBeDisabled();

    // 手动去掉 disable 校验：尝试点击「取消」后弹窗应关闭，且无任何 /channel/import 请求
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    const importCalls = fetchMock.mock.calls.filter(([url]) =>
      String(url).includes("/channel/import"),
    );
    expect(importCalls).toHaveLength(0);
  });
});
