import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import CustomModelsPage from "./CustomModels";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import { sampleChannel } from "@/test/fixtures/channels";

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

const customChannel = {
  ...sampleChannel,
  id: 5,
  name: "welcome-bot",
  type: "custom" as const,
  base_url: "",
  key: "",
  keys: [],
  models: [{ id: 50, channel_id: 5, name: "welcome-model", source: "manual" as const }],
  fixed_reply: "您好，欢迎咨询！",
};

beforeEach(() => {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
  );
});

describe("<CustomModelsPage />", () => {
  it("只列出 type=custom 的渠道，普通渠道不出现", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/channel/list")) {
          return Promise.resolve(jsonOk([sampleChannel, customChannel]));
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<CustomModelsPage />, { wrapper: Wrapper });

    await waitFor(() =>
      expect(screen.getByText("welcome-bot")).toBeInTheDocument(),
    );
    expect(screen.getByText("welcome-model")).toBeInTheDocument();
    expect(screen.getByText("您好，欢迎咨询！")).toBeInTheDocument();
    // 普通渠道被过滤
    expect(screen.queryByText("openai-prod")).not.toBeInTheDocument();
    expect(
      screen.getByRole("switch", { name: "启用自定义模型 welcome-bot" }),
    ).toBeInTheDocument();
  });

  it("新建弹窗初始不报错，点保存后才提示且不发创建请求", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string) => {
      if (url.includes("/channel/list")) return Promise.resolve(jsonOk([]));
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<CustomModelsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /新建自定义模型/ }));
    await waitFor(() => screen.getByRole("dialog"));

    expect(screen.queryAllByRole("alert")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "保存" }));

    const alerts = await screen.findAllByRole("alert");
    expect(alerts.map((a) => a.textContent)).toEqual(
      expect.arrayContaining(["必填", "模型名不能为空", "固定回复不能为空"]),
    );
    expect(
      fetchMock.mock.calls.some((c) => String(c[0]).includes("/channel/create")),
    ).toBe(false);
  });

  it("新建：提交 type=custom + fixed_reply + 单模型", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/channel/list")) return Promise.resolve(jsonOk([]));
      if (url.includes("/channel/create") && method === "POST") {
        return Promise.resolve(jsonOk(customChannel));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<CustomModelsPage />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: /新建自定义模型/ }));
    await waitFor(() => screen.getByRole("dialog"));

    // Field 的 label 文本含 required 星号与 sr-only「必填」，须用前缀正则匹配
    await user.type(screen.getByLabelText(/^名称/), "客服欢迎");
    await user.type(screen.getByLabelText(/^模型名/), "welcome-model");
    await user.type(screen.getByLabelText(/^固定回复/), "您好！");

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) =>
        String(c[0]).includes("/channel/create"),
      );
      expect(call).toBeTruthy();
      const body = JSON.parse((call![1] as RequestInit).body as string);
      expect(body.type).toBe("custom");
      expect(body.fixed_reply).toBe("您好！");
      expect(body.models[0].name).toBe("welcome-model");
      expect(body.base_url).toBe("");
    });
  });

  it("编辑：模型名锁定不可修改", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/channel/list")) {
          return Promise.resolve(jsonOk([customChannel]));
        }
        return Promise.resolve(jsonOk(null));
      }),
    );
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("welcome-bot"));

    await user.click(screen.getByRole("button", { name: /编辑自定义模型 welcome-bot/ }));
    await waitFor(() => screen.getByRole("dialog"));

    const modelInput = screen.getByLabelText(/^模型名/) as HTMLInputElement;
    expect(modelInput).toBeDisabled();
    expect(modelInput.value).toBe("welcome-model");
  });

  it("删除走两段确认后调 DELETE", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/channel/list")) return Promise.resolve(jsonOk([customChannel]));
      if (url.includes("/channel/delete/5") && method === "DELETE") {
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("welcome-bot"));

    await user.click(screen.getByRole("button", { name: /删除自定义模型 welcome-bot/ }));
    await waitFor(() => screen.getByRole("dialog"));
    await user.click(screen.getByRole("button", { name: "删除" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) =>
        String(c[0]).includes("/channel/delete/5"),
      );
      expect(call).toBeTruthy();
      expect((call![1] as RequestInit).method).toBe("DELETE");
    });
  });
});

describe("<CustomModelsPage /> 搜索与排序", () => {
  const chA = {
    ...sampleChannel,
    id: 10,
    name: "alpha-bot",
    type: "custom" as const,
    base_url: "",
    key: "",
    keys: [],
    models: [{ id: 100, channel_id: 10, name: "alpha-model", source: "manual" as const }],
    fixed_reply: "Alpha reply",
    sort: 5,
  };
  const chB = {
    ...sampleChannel,
    id: 11,
    name: "beta-bot",
    type: "custom" as const,
    base_url: "",
    key: "",
    keys: [],
    models: [{ id: 101, channel_id: 11, name: "beta-model", source: "manual" as const }],
    fixed_reply: "Beta reply",
    sort: -3,
  };
  const chC = {
    ...sampleChannel,
    id: 12,
    name: "gamma-bot",
    type: "custom" as const,
    base_url: "",
    key: "",
    keys: [],
    models: [{ id: 102, channel_id: 12, name: "gamma-model", source: "manual" as const }],
    fixed_reply: "Gamma reply",
    sort: 5, // 与 chA 相同 — 验证重复值
  };

  function mockList(channels: unknown[]) {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/channel/list")) return Promise.resolve(jsonOk(channels));
        return Promise.resolve(jsonOk(null));
      }),
    );
  }

  it("搜索按名称过滤", async () => {
    const user = userEvent.setup();
    mockList([chA, chB, chC]);
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("alpha-bot"));

    const searchInput = screen.getByLabelText("搜索自定义模型");
    await user.type(searchInput, "beta");
    await waitFor(() => {
      expect(screen.getByText("beta-bot")).toBeInTheDocument();
      expect(screen.queryByText("alpha-bot")).not.toBeInTheDocument();
      expect(screen.queryByText("gamma-bot")).not.toBeInTheDocument();
    });
  });

  it("搜索按模型名过滤", async () => {
    const user = userEvent.setup();
    mockList([chA, chB, chC]);
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("alpha-bot"));

    const searchInput = screen.getByLabelText("搜索自定义模型");
    await user.type(searchInput, "gamma-model");
    await waitFor(() => {
      expect(screen.getByText("gamma-bot")).toBeInTheDocument();
      expect(screen.queryByText("alpha-bot")).not.toBeInTheDocument();
    });
  });

  it("自定义排序: 按 sort 值降序, 同值按名称", async () => {
    mockList([chA, chB, chC]);
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("alpha-bot"));

    // chA & chC sort=5 在前（同值 alpha < gamma 按名称）, chB sort=-3 在后
    const rows = screen.getAllByText(/-bot$/).map((el) => el.textContent);
    expect(rows).toEqual(["alpha-bot", "gamma-bot", "beta-bot"]);
  });

  it("优先级行内编辑触发 update 请求", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.includes("/channel/list")) return Promise.resolve(jsonOk([chA, chB, chC]));
      if (url.includes("/channel/update")) return Promise.resolve(jsonOk(chA));
      return Promise.resolve(jsonOk(null));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("alpha-bot"));

    const input = screen.getByLabelText("优先级 alpha-bot") as HTMLInputElement;
    expect(input.value).toBe("5");
    await user.clear(input);
    await user.type(input, "-10");
    await user.tab();

    await waitFor(() => {
      const updateCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).includes("/channel/update"),
      );
      expect(updateCalls.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(
        updateCalls[updateCalls.length - 1][1]?.body as string,
      ) as { id: number; sort: number };
      expect(body.id).toBe(10);
      expect(body.sort).toBe(-10);
    });
  });

  it("切换为按名称排序", async () => {
    const user = userEvent.setup();
    mockList([chA, chB, chC]);
    render(<CustomModelsPage />, { wrapper: Wrapper });
    await waitFor(() => screen.getByText("alpha-bot"));

    await user.selectOptions(screen.getByLabelText("排序"), "name");
    const rows = screen.getAllByText(/-bot$/).map((el) => el.textContent);
    expect(rows).toEqual(["alpha-bot", "beta-bot", "gamma-bot"]);
  });
});
