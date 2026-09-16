import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import GroupsPage from "./Groups";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import { sampleChannel, sampleGroup } from "@/test/fixtures/channels";

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

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

// GroupsPage 挂载即订阅 /group/runtime/stream（冷却/亲和倒计时）：
// jsdom 没有 EventSource，统一用假实现，事件由用例手动注入。
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  withCredentials = false;
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  listeners: Record<string, ((e: Event) => void)[]> = {};

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = !!init?.withCredentials;
    FakeEventSource.instances.push(this);
  }
  addEventListener(name: string, fn: (e: Event) => void) {
    (this.listeners[name] ||= []).push(fn);
  }
  close() {}
  fireEvent(name: string, data: unknown) {
    const event = new MessageEvent(name, { data: JSON.stringify(data) });
    this.listeners[name]?.forEach((fn) => fn(event));
  }
}

beforeEach(() => {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
  vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
  FakeEventSource.instances = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});



describe("GroupEditor relay_config 默认值契约", () => {
  function setupGroupFetch(groups: unknown[]) {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/group/list")) return Promise.resolve(jsonOk(groups));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  it("新建分组：表单默认值取 DEFAULT_GROUP_RELAY_CONFIG（粘合开、轮次 600），不是 false/60", async () => {
    const user = userEvent.setup();
    setupGroupFetch([]);
    render(<GroupsPage />, { wrapper: Wrapper });

    await user.click(screen.getByRole("button", { name: "新建分组" }));
    await waitFor(() => screen.getByRole("dialog"));

    // 路由策略字段在「路由策略」Tab 内，切过去才能看到
    await user.click(screen.getByRole("button", { name: "路由策略" }));

    // sampleGroup 的服务端值是 60；若误用旧的手写默认 60 会与后端默认 600 混淆不了，
    // 所以这里断言 600 —— 只有真正引用 DEFAULT_GROUP_RELAY_CONFIG 才能得到。
    const maxRounds = screen.getByLabelText(/单请求最大轮次/) as HTMLInputElement;
    expect(maxRounds.value).toBe("600");
    const cooldown = screen.getByLabelText(/^冷却时间/) as HTMLInputElement;
    expect(cooldown.value).toBe("60");
    // 路由策略页有 会话粘合 / 后台定时探测 / 协议透传偏好 / 启用脱敏 四个开关；粘合默认开
    const switches = screen.getAllByRole("switch");
    expect(switches.length).toBe(4);
    expect(switches[0]).toHaveAttribute("aria-checked", "true");
  });

  it("编辑已有分组：读服务端 relay_config，不被默认值覆盖", async () => {
    const user = userEvent.setup();
    // sampleGroup.relay_config: session_sticky_enabled=false, max_request_rounds=60
    setupGroupFetch([sampleGroup]);
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // 路由策略字段在「路由策略」Tab 内
    await user.click(screen.getByRole("button", { name: "路由策略" }));

    const maxRounds = screen.getByLabelText(/单请求最大轮次/) as HTMLInputElement;
    expect(maxRounds.value).toBe("60");
    const switches = screen.getAllByRole("switch");
    expect(switches[0]).toHaveAttribute("aria-checked", "false");
  });
});

describe("GroupEditor 保存校验", () => {
  it("非法名称（NAME_RULE 不通过）时保存禁用，红字提示不可绕过", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<GroupsPage />, { wrapper: Wrapper });

    await user.click(screen.getByRole("button", { name: "新建分组" }));
    await waitFor(() => screen.getByRole("dialog"));

    const save = screen.getByRole("button", { name: "保存" });
    expect(save).toBeDisabled(); // 名称为空

    // Field 的 label 文本含 required 星号与 sr-only「必填」，须用前缀正则匹配
    await user.type(screen.getByLabelText(/^名称/), "a!b");
    expect(screen.getByText(/只能包含字母、数字、中文/)).toBeInTheDocument();
    expect(save).toBeDisabled();

    await user.clear(screen.getByLabelText(/^名称/));
    await user.type(screen.getByLabelText(/^名称/), "合法名称");
    expect(save).toBeEnabled();
  });
});

describe("buildMemberDiff 行为（间接通过 add/remove 后保存）", () => {
  it("新增成员 → 走 items_to_add；删除 → items_to_delete", async () => {
    const user = userEvent.setup();
    const calls: Array<{ url: string; body?: any }> = [];
    // 仅含一个成员（model 100）的分组，使 gpt-4o-mini(101) 可作为新成员添加
    const groupWithOneItem = {
      ...sampleGroup,
      items: [
        { id: 1, group_id: 10, channel_model_id: 100, ref_group_name: "", priority: 1 },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const body = init?.body ? JSON.parse(String(init.body)) : null;
        calls.push({ url, body });
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([groupWithOneItem]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        if (url.includes("/group/update") && init?.method === "POST") {
          return Promise.resolve(jsonOk(groupWithOneItem));
        }
        if (url.includes("/group/cooldown/clear/")) return Promise.resolve(jsonOk(null));
        if (url.includes("/group/active/")) return Promise.resolve(jsonOk(null));
        return Promise.resolve(jsonOk(null));
      }),
    );

    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    // 打开编辑
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // 左栏选择器：渠道默认折叠，先展开 openai-prod 再点击模型加入成员
    await user.click(
      screen.getByRole("button", { name: "渠道 openai-prod" }),
    );
    await user.click(
      screen.getByRole("button", { name: "添加 openai-prod gpt-4o-mini" }),
    );

    // 现在应该有 2 个成员，名次输入框分别显示 1 和 2
    await waitFor(() => {
      const positionInputs = screen.getAllByLabelText(/的排序名次/);
      expect(positionInputs).toHaveLength(2);
      expect(positionInputs[1]).toHaveValue(2);
    });

    // 保存
    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    // 验证调用：body 应含 items_to_add
    await waitFor(() => {
      const updateCall = calls.find((c) => c.url.includes("/group/update"));
      expect(updateCall).toBeTruthy();
      expect(updateCall!.body).toHaveProperty("items_to_add");
      expect((updateCall!.body as any).items_to_add).toHaveLength(1);
      expect((updateCall!.body as any).items_to_add[0]).toEqual({
        channel_model_id: 101,
        ref_group_name: "",
        priority: 2,
      });
    });
  });
});

describe("手动模式当前成员：新增成员即可指定，保存才生效", () => {
  // manual 分组 + 一个已保存成员（cm 100），使 cm 101 可作为新成员添加
  const manualGroup = {
    ...sampleGroup,
    mode: "manual",
    active_item_id: 1,
    items: [
      { id: 1, group_id: 10, channel_model_id: 100, ref_group_name: "", priority: 1 },
    ],
  };

  it("选中未保存的新成员为「当前」→ 单选可用且选中；保存后才调 setActive，用解析出的新 id", async () => {
    const user = userEvent.setup();
    const calls: Array<{ url: string; body?: any }> = [];
    // update 响应：新成员获得 id=2（priority 与提交一致）
    const updatedGroup = {
      ...manualGroup,
      items: [
        { id: 1, group_id: 10, channel_model_id: 100, ref_group_name: "", priority: 1 },
        { id: 2, group_id: 10, channel_model_id: 101, ref_group_name: "", priority: 2 },
      ],
    };

    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const body = init?.body ? JSON.parse(String(init.body)) : null;
        calls.push({ url, body });
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([manualGroup]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        if (url.includes("/group/update") && init?.method === "POST")
          return Promise.resolve(jsonOk(updatedGroup));
        if (url.includes("/group/active/")) return Promise.resolve(jsonOk(updatedGroup));
        if (url.includes("/group/cooldown/clear/")) return Promise.resolve(jsonOk(null));
        return Promise.resolve(jsonOk(null));
      }),
    );

    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // 展开渠道并添加 gpt-4o-mini（cm 101）作为新成员
    await user.click(screen.getByRole("button", { name: "渠道 openai-prod" }));
    await user.click(screen.getByRole("button", { name: "添加 openai-prod gpt-4o-mini" }));

    // 核心断言①：新成员（未保存，id=0）的「设为当前」单选未被禁用
    const radioNew = screen.getByLabelText(/设为当前成员.*#101/);
    expect(radioNew).not.toBeDisabled();

    // 选中新成员为当前，并出现「保存后生效」提示
    await user.click(radioNew);
    expect(radioNew).toBeChecked();
    expect(screen.getByText(/保存后生效/)).toBeInTheDocument();

    // 核心断言②：选中后、保存前不调任何 active 接口（保存才生效）
    expect(calls.find((c) => c.url.includes("/group/active/"))).toBeUndefined();

    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    // 核心断言③：保存后 setActive 用新成员解析出的 id=2（既不是 0，也不是旧的 1）
    await waitFor(() => {
      const activeCall = calls.find((c) => c.url.includes("/group/active/"));
      expect(activeCall).toBeTruthy();
      expect((activeCall!.body as any).item_id).toBe(2);
    });
  });

  it("新建 manual 分组：选中第二个未保存成员为「当前」→ 创建后 setActive 用对应 id（非默认第一个）", async () => {
    const user = userEvent.setup();
    const calls: Array<{ url: string; body?: any }> = [];
    // create 响应：两个成员按 priority 获得 id=11 / 12
    const createdGroup = {
      id: 30,
      name: "new-manual",
      mode: "manual",
      active_item_id: 0,
      relay_config: { ...sampleGroup.relay_config },
      items: [
        { id: 11, group_id: 30, channel_model_id: 100, ref_group_name: "", priority: 1 },
        { id: 12, group_id: 30, channel_model_id: 101, ref_group_name: "", priority: 2 },
      ],
    };

    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const body = init?.body ? JSON.parse(String(init.body)) : null;
        calls.push({ url, body });
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        if (url.includes("/group/create") && init?.method === "POST")
          return Promise.resolve(jsonOk(createdGroup));
        if (url.includes("/group/active/")) return Promise.resolve(jsonOk(createdGroup));
        return Promise.resolve(jsonOk(null));
      }),
    );

    render(<GroupsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "新建分组" }));
    await waitFor(() => screen.getByRole("dialog"));

    // 新建默认 manual 模式；填名称
    await user.type(screen.getByLabelText(/^名称/), "new-manual");

    // 依次添加 gpt-4o(100) 与 gpt-4o-mini(101)
    await user.click(screen.getByRole("button", { name: "渠道 openai-prod" }));
    await user.click(screen.getByRole("button", { name: "添加 openai-prod gpt-4o" }));
    await user.click(screen.getByRole("button", { name: "添加 openai-prod gpt-4o-mini" }));

    // 选中第二个成员（#101）为当前
    const radioSecond = screen.getByLabelText(/设为当前成员.*#101/);
    await user.click(radioSecond);
    expect(radioSecond).toBeChecked();

    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    // 创建后 setActive 用第二个成员的 id=12，而非默认的第一个 id=11
    await waitFor(() => {
      const activeCall = calls.find((c) => c.url.includes("/group/active/"));
      expect(activeCall).toBeTruthy();
      expect((activeCall!.body as any).item_id).toBe(12);
    });
  });
});

describe("分组卡片冷却/亲和实时倒计时", () => {
  function fireRuntime(payload: Record<string, unknown>) {
    act(() => {
      FakeEventSource.instances[0]?.fireEvent("runtime", payload);
    });
  }
  function stubListFetch() {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([sampleGroup]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  it("成员冷却中显示倒计时 chip，清零后消失", async () => {
    stubListFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    fireRuntime({
      group_id: 10,
      current_item_id: 0,
      probe_item_id: 0,
      affinity_until: 0,
      cooldowns: { "1": Date.now() + 30_000 },
      levels: {},
      half_opens: {},
      post_commit_strikes: {},
      emergency_item_id: 0,
      emergency_active: 0,
    });
    // ceil 取整，秒边界在 30s/29s 之间抖动
    await waitFor(() =>
      expect(screen.getByText(/冷却 29s|冷却 30s/)).toBeInTheDocument(),
    );

    // 冷却到期（前端按当前时间忽略过期条目）：chip 消失
    fireRuntime({
      group_id: 10,
      current_item_id: 0,
      probe_item_id: 0,
      affinity_until: 0,
      cooldowns: { "1": Date.now() - 1_000 },
      levels: {},
      half_opens: {},
      post_commit_strikes: {},
      emergency_item_id: 0,
      emergency_active: 0,
    });
    await waitFor(() =>
      expect(screen.queryByText(/冷却 \d/)).not.toBeInTheDocument(),
    );
  });

  it("当前承载成员显示亲和倒计时；紧急兜底显示卡片级标记", async () => {
    stubListFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    fireRuntime({
      group_id: 10,
      current_item_id: 2,
      probe_item_id: 0,
      affinity_until: Date.now() + 300_000,
      cooldowns: {},
      levels: {},
      half_opens: {},
      post_commit_strikes: {},
      emergency_item_id: 2,
      emergency_active: 1,
    });
    await waitFor(() =>
      expect(screen.getByText(/亲和 5:00|亲和 4:59/)).toBeInTheDocument(),
    );
    // 亲和只属于 current_item_id（成员 2），成员 1 不显示
    expect(screen.queryByText(/冷却 \d/)).not.toBeInTheDocument();
    expect(screen.getByText("紧急兜底")).toBeInTheDocument();
  });
});

describe("分组列表排序与自定义顺序", () => {
  beforeEach(() => {
    // jsdom localStorage 跨用例共享，清掉排序偏好避免用例间串扰
    localStorage.removeItem("nv-group-sort");
  });

  let fetchMock: ReturnType<typeof vi.fn>;
  let serverGroups: Array<Record<string, unknown>>;

  function groupJson(
    id: number,
    name: string,
    extra: Record<string, unknown> = {},
  ) {
    return {
      id,
      name,
      mode: "failover",
      active_item_id: 0,
      relay_config: { ...sampleGroup.relay_config },
      items: [] as unknown[],
      ...extra,
    };
  }

  /** mock 后端：/group/list 返回 serverGroups；/group/update 写入 display_order
   * 并按其排序（模拟后端 GroupList 的 display_order 排序契约）。 */
  function setupSortFetch() {
    fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.includes("/group/list")) return Promise.resolve(jsonOk(serverGroups));
      if (url.includes("/channel/list")) return Promise.resolve(jsonOk([sampleChannel]));
      if (url.includes("/group/update")) {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          id: number;
          display_order?: number;
        };
        serverGroups = serverGroups
          .map((g) =>
            g.id === body.id ? { ...g, display_order: body.display_order } : g,
          )
          .sort(
            (a, b) =>
              (Number(a.display_order) || Number.MAX_SAFE_INTEGER) -
              (Number(b.display_order) || Number.MAX_SAFE_INTEGER),
          );
        return Promise.resolve(jsonOk(null));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);
  }

  function updateBodies() {
    return fetchMock.mock.calls
      .filter((call) => String(call[0]).includes("/group/update"))
      .map((call) => JSON.parse(String(call[1]?.body)));
  }

  function expectOrderedInDOM(a: HTMLElement, b: HTMLElement) {
    // 断言 a 在 b 之前（同一文档中的先后关系）
    expect(
      !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING),
    ).toBe(true);
  }

  it("切换为按名称排序立即生效并写入 localStorage，重新挂载后保持", async () => {
    const user = userEvent.setup();
    serverGroups = [
      groupJson(31, "c-group"),
      groupJson(32, "a-group"),
      groupJson(33, "b-group"),
    ];
    setupSortFetch();
    const { unmount } = render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("a-group"));

    // 默认按优先级；成员全空时比较恒为 0，稳定排序保持后端返回顺序 c → a → b
    expectOrderedInDOM(screen.getByText("c-group"), screen.getByText("a-group"));
    expectOrderedInDOM(screen.getByText("a-group"), screen.getByText("b-group"));

    await user.selectOptions(screen.getByLabelText("分组排序方式"), "name");
    expect(localStorage.getItem("nv-group-sort")).toBe("name");
    expectOrderedInDOM(screen.getByText("a-group"), screen.getByText("b-group"));
    expectOrderedInDOM(screen.getByText("b-group"), screen.getByText("c-group"));

    unmount();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("a-group"));
    expect(
      (screen.getByLabelText("分组排序方式") as HTMLSelectElement).value,
    ).toBe("name");
    expectOrderedInDOM(screen.getByText("a-group"), screen.getByText("b-group"));
    expectOrderedInDOM(screen.getByText("b-group"), screen.getByText("c-group"));
  });

  it("按优先级排序：分组内成员最小 priority 在前", async () => {
    serverGroups = [
      groupJson(41, "晚接管组", {
        items: [
          { id: 1, group_id: 41, channel_model_id: 100, ref_group_name: "", priority: 5 },
        ],
      }),
      groupJson(42, "先接管组", {
        items: [
          { id: 2, group_id: 42, channel_model_id: 100, ref_group_name: "", priority: 2 },
        ],
      }),
    ];
    setupSortFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("先接管组"));
    expectOrderedInDOM(screen.getByText("先接管组"), screen.getByText("晚接管组"));
  });

  it("custom 排序保持后端顺序；下移按全量重排提交且只更新变化的分组", async () => {
    const user = userEvent.setup();
    serverGroups = [
      groupJson(21, "甲组", { display_order: 1 }),
      groupJson(22, "乙组", { display_order: 2 }),
      groupJson(23, "丙组", { display_order: 3 }),
    ];
    setupSortFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("甲组"));

    await user.selectOptions(screen.getByLabelText("分组排序方式"), "custom");
    // custom 直接沿用后端 display_order 排序结果
    expectOrderedInDOM(screen.getByText("甲组"), screen.getByText("乙组"));
    // 首尾边界禁用，中间分组可移动
    expect(screen.getByLabelText("上移分组 甲组")).toBeDisabled();
    expect(screen.getByLabelText("下移分组 丙组")).toBeDisabled();
    expect(screen.getByLabelText("上移分组 乙组")).toBeEnabled();

    await user.click(screen.getByLabelText("下移分组 乙组"));

    // 目标顺序 甲(1) 丙(2) 乙(3)：乙 2→3、丙 3→2，甲不变不提交
    await waitFor(() => expect(updateBodies()).toHaveLength(2));
    expect(updateBodies()).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: 22, display_order: 3 }),
        expect.objectContaining({ id: 23, display_order: 2 }),
      ]),
    );
    expect(updateBodies().some((b) => b.id === 21)).toBe(false);

    // onSuccess invalidate 后重拉，展示顺序收敛为新顺序
    await waitFor(() => {
      expectOrderedInDOM(screen.getByText("甲组"), screen.getByText("丙组"));
      expectOrderedInDOM(screen.getByText("丙组"), screen.getByText("乙组"));
    });
  });

  it("localStorage 排序值非法时回退为按优先级", async () => {
    localStorage.setItem("nv-group-sort", "bogus");
    serverGroups = [groupJson(51, "唯一组")];
    setupSortFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("唯一组"));
    expect(
      (screen.getByLabelText("分组排序方式") as HTMLSelectElement).value,
    ).toBe("priority");
  });
});

describe("ChannelModelPicker 搜索过滤", () => {
  // 两个渠道，各自多个模型；两个可引用分组
  const ch1: typeof sampleChannel = {
    ...sampleChannel,
    id: 1,
    name: "openai-prod",
    models: [
      { id: 100, channel_id: 1, name: "gpt-4o", source: "auto" },
      { id: 101, channel_id: 1, name: "gpt-4o-mini", source: "auto" },
      { id: 102, channel_id: 1, name: "o1-preview", source: "auto" },
    ],
  };
  const ch2: typeof sampleChannel = {
    ...sampleChannel,
    id: 2,
    name: "anthropic-prod",
    models: [
      { id: 200, channel_id: 2, name: "claude-sonnet", source: "auto" },
      { id: 201, channel_id: 2, name: "claude-haiku", source: "auto" },
    ],
  };
  const refGroupA = { ...sampleGroup, id: 20, name: "ref-backup" };
  const refGroupB = { ...sampleGroup, id: 21, name: "ref-canary" };

  function setupSearchFetch() {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/group/list"))
          return Promise.resolve(jsonOk([sampleGroup, refGroupA, refGroupB]));
        if (url.includes("/channel/list"))
          return Promise.resolve(jsonOk([ch1, ch2]));
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  it("搜索模型名：只显示匹配的模型，非匹配模型不出现", async () => {
    const user = userEvent.setup();
    setupSearchFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getAllByRole("button", { name: /编辑/ })[0]);
    await waitFor(() => screen.getByRole("dialog"));

    const searchInput = screen.getByLabelText("搜索渠道或模型");
    await user.type(searchInput, "gpt-4o");

    // 搜索态自动展开命中渠道；gpt-4o 和 gpt-4o-mini 应出现，o1-preview 不应出现
    await waitFor(() =>
      expect(screen.getByText("gpt-4o")).toBeInTheDocument(),
    );
    expect(screen.getByText("gpt-4o-mini")).toBeInTheDocument();
    expect(screen.queryByText("o1-preview")).not.toBeInTheDocument();
    // anthropic 渠道不命中，其模型也不应出现
    expect(screen.queryByText("claude-sonnet")).not.toBeInTheDocument();
  });

  it("搜索渠道名：显示该渠道下全部模型（即使模型名不含搜索词）", async () => {
    const user = userEvent.setup();
    setupSearchFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getAllByRole("button", { name: /编辑/ })[0]);
    await waitFor(() => screen.getByRole("dialog"));

    const searchInput = screen.getByLabelText("搜索渠道或模型");
    // 搜索渠道名 "openai" —— 渠道名命中但模型名都不含 "openai"
    await user.type(searchInput, "openai");

    // 渠道名命中 → 该渠道下全部模型都应显示
    await waitFor(() =>
      expect(screen.getByText("gpt-4o")).toBeInTheDocument(),
    );
    expect(screen.getByText("gpt-4o-mini")).toBeInTheDocument();
    expect(screen.getByText("o1-preview")).toBeInTheDocument();
    // 不应出现"无匹配模型"提示
    expect(screen.queryByText("无匹配模型")).not.toBeInTheDocument();
    // anthropic 渠道不命中，其模型不应出现
    expect(screen.queryByText("claude-sonnet")).not.toBeInTheDocument();
  });

  it("搜索分组名：引用分组区域只显示匹配的分组", async () => {
    const user = userEvent.setup();
    setupSearchFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getAllByRole("button", { name: /编辑/ })[0]);
    await waitFor(() => screen.getByRole("dialog"));

    const searchInput = screen.getByLabelText("搜索渠道或模型");
    await user.type(searchInput, "canary");

    // ref-canary 命中，ref-backup 不命中
    await waitFor(() =>
      expect(screen.getByText("→ ref-canary")).toBeInTheDocument(),
    );
    expect(screen.queryByText("→ ref-backup")).not.toBeInTheDocument();
  });

  it("清空搜索：恢复全量展示", async () => {
    const user = userEvent.setup();
    setupSearchFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));

    await user.click(screen.getAllByRole("button", { name: /编辑/ })[0]);
    await waitFor(() => screen.getByRole("dialog"));

    const searchInput = screen.getByLabelText("搜索渠道或模型");
    await user.type(searchInput, "claude");
    await waitFor(() =>
      expect(screen.getByText("claude-sonnet")).toBeInTheDocument(),
    );
    // 搜索 claude 时 openai-prod 渠道不命中，不应出现
    expect(screen.queryByText("openai-prod")).not.toBeInTheDocument();

    // 清空搜索后所有渠道恢复（渠道名始终可见）
    await user.clear(searchInput);
    await waitFor(() =>
      expect(screen.getByText("openai-prod")).toBeInTheDocument(),
    );
    expect(screen.getByText("anthropic-prod")).toBeInTheDocument();
    // 「没有匹配」提示消失
    expect(screen.queryByText("没有匹配的渠道或模型")).not.toBeInTheDocument();
  });
});

describe("GroupEditor 成员名次输入", () => {
  // 3 模型渠道 + 3 成员分组，用于测试名次重排
  const channel3 = {
    ...sampleChannel,
    models: [
      { id: 100, channel_id: 1, name: "gpt-4o", source: "auto" },
      { id: 101, channel_id: 1, name: "gpt-4o-mini", source: "auto" },
      { id: 102, channel_id: 1, name: "gpt-4o-large", source: "auto" },
    ],
  };
  const group3 = {
    ...sampleGroup,
    items: [
      { id: 1, group_id: 10, channel_model_id: 100, ref_group_name: "", priority: 1 },
      { id: 2, group_id: 10, channel_model_id: 101, ref_group_name: "", priority: 2 },
      { id: 3, group_id: 10, channel_model_id: 102, ref_group_name: "", priority: 3 },
    ],
  };

  function stubFetch(capture?: Array<{ url: string; body?: any }>) {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const body = init?.body ? JSON.parse(String(init.body)) : null;
        capture?.push({ url, body });
        if (url.includes("/group/list")) return Promise.resolve(jsonOk([group3]));
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk([channel3]));
        if (url.includes("/group/update") && init?.method === "POST")
          return Promise.resolve(jsonOk(group3));
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  // 获取名次输入框数组（DOM 顺序 = 当前排列顺序）
  function positionInputs() {
    return screen.getAllByLabelText(/的排序名次/) as HTMLInputElement[];
  }

  // 断言成员排列顺序（按 channel_model_id）
  function expectOrder(...ids: number[]) {
    const inputs = positionInputs();
    expect(inputs).toHaveLength(ids.length);
    ids.forEach((id, i) => {
      // #id 后跟非数字字符，兼容「渠道模型 #102」与「openai-prod → gpt-4o（#102）」两种标签
      expect(inputs[i].getAttribute("aria-label")).toMatch(new RegExp(`#${id}[^0-9]`));
    });
  }

  it("最后一位输入 1 → 排到第一位，其余顺延", async () => {
    const user = userEvent.setup();
    stubFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    expectOrder(100, 101, 102);

    // 在 #102（第三位）的名次输入框中输入 1 并失焦提交
    const input102 = screen.getByLabelText(/#102.*的排序名次/);
    fireEvent.change(input102, { target: { value: "1" } });
    fireEvent.blur(input102);

    // #102 移到第一位，#100 和 #101 顺延
    expectOrder(102, 100, 101);
    expect(positionInputs().map((i) => i.value)).toEqual(["1", "2", "3"]);
  });

  it("回车键提交名次变更", async () => {
    const user = userEvent.setup();
    stubFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // 在 #100（第一位）输入 3 并按回车
    const input100 = screen.getByLabelText(/#100.*的排序名次/);
    fireEvent.change(input100, { target: { value: "3" } });
    input100.focus();
    fireEvent.keyDown(input100, { key: "Enter" });

    // #100 移到第三位
    expectOrder(101, 102, 100);
  });

  it("输入超过总数 → 夹到最后一位", async () => {
    const user = userEvent.setup();
    stubFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    const input100 = screen.getByLabelText(/#100.*的排序名次/);
    fireEvent.change(input100, { target: { value: "99" } });
    fireEvent.blur(input100);

    // #100 夹到最后
    expectOrder(101, 102, 100);
  });

  it("输入 0 → 不移动，值回退原位", async () => {
    const user = userEvent.setup();
    stubFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    const input101 = screen.getByLabelText(/#101.*的排序名次/);
    fireEvent.change(input101, { target: { value: "0" } });
    fireEvent.blur(input101);

    // 顺序不变，值回退
    expectOrder(100, 101, 102);
    expect(input101).toHaveValue(2);
  });

  it("Escape → 取消编辑，值回退，顺序不变", async () => {
    const user = userEvent.setup();
    stubFetch();
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    const input102 = screen.getByLabelText(/#102.*的排序名次/);
    fireEvent.change(input102, { target: { value: "1" } });
    input102.focus();
    fireEvent.keyDown(input102, { key: "Escape" });

    // 顺序不变，值回退
    expectOrder(100, 101, 102);
    expect(input102).toHaveValue(3);
  });

  it("名次改动不立即调 API → 点保存才提交 items_to_update", async () => {
    const user = userEvent.setup();
    const calls: Array<{ url: string; body?: any }> = [];
    stubFetch(calls);
    render(<GroupsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("gpt-4o-prod"));
    await user.click(screen.getByRole("button", { name: /编辑/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // 移动 #102 到第 1 位
    const input102 = screen.getByLabelText(/#102.*的排序名次/);
    fireEvent.change(input102, { target: { value: "1" } });
    fireEvent.blur(input102);
    expectOrder(102, 100, 101);

    // 尚未点保存 → 不应有 /group/update 调用
    expect(calls.find((c) => c.url.includes("/group/update"))).toBeUndefined();

    // 保存
    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    // items_to_update：3 个成员的 priority 都变了
    // #100: 1→2, #101: 2→3, #102: 3→1
    await waitFor(() => {
      const updateCall = calls.find((c) => c.url.includes("/group/update"));
      expect(updateCall).toBeTruthy();
      const updates = (updateCall!.body as any).items_to_update;
      expect(updates).toHaveLength(3);
      const byId = [...updates].sort((a: any, b: any) => a.id - b.id);
      expect(byId[0]).toEqual({ id: 1, priority: 2 });
      expect(byId[1]).toEqual({ id: 2, priority: 3 });
      expect(byId[2]).toEqual({ id: 3, priority: 1 });
    });
  });
});
