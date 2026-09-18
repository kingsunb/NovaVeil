import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  api,
  apiForbiddenEvent,
  apiUnauthorizedEvent,
  APIError,
  parseHeaderTemplates,
} from "./api";

// 信封格式：{code, message, data}
const ok = <T,>(data: T) =>
  new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });

describe("api.getNowVersion", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok({
            version: "0.13.0",
            commit: "abc",
            build_time: "2025-01-01",
            client_ip_count: 42,
            total_requests: 1024,
            error_count: 0,
            total_tokens_input: 0,
            total_tokens_output: 0,
            tokens_by_model: [],
          }),
        ),
      ),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("GET /api/v1/stats/now-version，拆 data 信封", async () => {
    const r = await api.getNowVersion();
    expect(r.version).toBe("0.13.0");
    expect(r.client_ip_count).toBe(42);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/stats/now-version",
      expect.objectContaining({ credentials: "include" }),
    );
  });
});

describe("api.createChannel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok({
            id: 1,
            name: "test",
            type: "openai",
            enabled: true,
            is_free: false,
            builtin: false,
            base_url: "https://api.example.com",
            key: "",
            keys: [],
            models: [],
            proxy: false,
            auto_sync: false,
            opencode_compat: false,
            pass_through_body_enabled: false,
            custom_header: [],
            model_limits: {},
            tags: [],
            sort: 0,
            rate_limit_rpm: 0,
            max_concurrent: 0,
          }),
        ),
      ),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("POST /api/v1/channel/create，body 走 JSON.stringify", async () => {
    await api.createChannel({
      name: "test",
      type: "openai",
      enabled: true,
      is_free: false,
      builtin: false,
      base_url: "https://api.example.com",
      key: "",
      keys: [],
      models: [],
      fixed_reply: "",
      proxy: false,
      auto_sync: false,
      opencode_compat: false,
      pass_through_body_enabled: false,
      custom_header: [],
      model_limits: {},
      tags: [],
      sort: 0,
      rate_limit_rpm: 60,
      max_concurrent: 5,
    });
    const call = (fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call[0]).toBe("/api/v1/channel/create");
    expect(call[1].method).toBe("POST");
    expect(JSON.parse(call[1].body).name).toBe("test");
  });
});

describe("api.deleteChannel", () => {
  it("DELETE /api/v1/channel/delete/:id，204 走 no-content 分支返回 undefined", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(null, { status: 204, headers: { "content-type": "application/json" } }))),
    );
    const r = await api.deleteChannel(1);
    expect(r).toBeUndefined();
    const call = (fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call[0]).toBe("/api/v1/channel/delete/1");
    expect(call[1].method).toBe("DELETE");
    vi.unstubAllGlobals();
  });
});

describe("api.listKeys", () => {
  it("GET /api/v1/apikey/list，拆出 api_key_masked 列表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            {
              id: 1,
              name: "k1",
              api_key_masked: "sk-Nv…KD6S",
              enabled: true,
              expire_at: 0,
              supported_models: "",
            },
          ]),
        ),
      ),
    );
    const r = await api.listKeys();
    expect(r).toHaveLength(1);
    expect(r[0].api_key_masked).toBe("sk-Nv…KD6S");
    vi.unstubAllGlobals();
  });
});

describe("api.createKey", () => {
  it("POST /api/v1/apikey/create，返回明文 + 摘要", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok({
            id: 7,
            name: "k1",
            api_key: "sk-NEW-1234",
            api_key_masked: "sk-Nv…1234",
            enabled: true,
            expire_at: 0,
            supported_models: "",
          }),
        ),
      ),
    );
    const r = await api.createKey({
      name: "k1",
      api_key: "",
      enabled: true,
      expire_at: 0,
      supported_models: "",
      max_concurrent: 0,
      rate_limit_rpm: 0,
    });
    expect(r.api_key).toBe("sk-NEW-1234");
    vi.unstubAllGlobals();
  });
});

describe("api.stopAll", () => {
  it("POST /api/v1/log/stop-all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(ok(null))),
    );
    await api.stopAll();
    const call = (fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call[0]).toBe("/api/v1/log/stop-all");
    expect(call[1].method).toBe("POST");
    vi.unstubAllGlobals();
  });
});

describe("错误路径", () => {
  it("非 2xx 抛 APIError，body 透传", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 500, message: "internal", data: null }),
            { status: 500, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    await expect(api.listChannels()).rejects.toBeInstanceOf(APIError);
    try {
      await api.listChannels();
    } catch (e) {
      expect(e).toBeInstanceOf(APIError);
      const err = e as APIError;
      expect(err.status).toBe(500);
      expect(err.statusText).toBe("internal");
      expect(err.body).toBeNull();
    }
    vi.unstubAllGlobals();
  });

  it("401 走 message 字段", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 401, message: "unauthorized", data: null }),
            { status: 401, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    await expect(api.status()).rejects.toMatchObject({
      status: 401,
      statusText: "unauthorized",
    });
    vi.unstubAllGlobals();
  });

  it("无效 JSON 抛 APIError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response("not json", { status: 200, headers: { "content-type": "application/json" } }),
        ),
      ),
    );
    await expect(api.status()).rejects.toBeInstanceOf(APIError);
    vi.unstubAllGlobals();
  });
});

describe("api.testChannel / testChannelKeys", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("testChannel → POST /channel/test，elapsed_ms 归一化为 latency_ms", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(
        ok({
          model: "gpt-4o",
          content: "走路",
          elapsed_ms: 812,
          prompt_tokens: 9,
          completion_tokens: 3,
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const r = await api.testChannel(5, "gpt-4o");
    expect(r.latency_ms).toBe(812);
    expect(r).not.toHaveProperty("ok");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/channel/test",
      expect.objectContaining({ method: "POST" }),
    );
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body).toEqual({ id: 5, model: "gpt-4o" });
  });

  it("testChannel 透传 key_id（按密钥测试）", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) => Promise.resolve(ok({ elapsed_ms: 1 })));
    vi.stubGlobal("fetch", fetchMock);

    await api.testChannel(5, "gpt-4o", undefined, "key-abc");
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body.key_id).toBe("key-abc");
  });

  it("testChannelKeys → POST /channel/test_keys，逐 Key 结果透传", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(
        ok([
          { key_id: "k1", label: "#1(主用)", ok: true, content: "走路", elapsed_ms: 812 },
          { key_id: "k2", label: "#2(备)", ok: false, error: "401", elapsed_ms: 90 },
        ]),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const r = await api.testChannelKeys(5, "gpt-4o");
    expect(r).toHaveLength(2);
    expect(r[0]).toMatchObject({ key_id: "k1", ok: true });
    expect(r[1]).toMatchObject({ key_id: "k2", ok: false });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/channel/test_keys",
      expect.objectContaining({ method: "POST" }),
    );
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body).toEqual({ id: 5, model: "gpt-4o" });
  });
});

describe("api.exportChannels / exportSettings 文件名", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("exportChannels 解析 Content-Disposition 文件名", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(
        new Response("dump", {
          status: 200,
          headers: {
            "content-disposition": 'attachment; filename="channels-20260102-150405.txt"',
          },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const r = await api.exportChannels();
    expect(r.text).toBe("dump");
    expect(r.filename).toBe("channels-20260102-150405.txt");
  });

  it("无 Content-Disposition 时 filename 为 undefined", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(new Response("dump", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const r = await api.exportChannels();
    expect(r.filename).toBeUndefined();
  });

  it("exportSettings 返回解析后的 DBDump 与文件名", async () => {
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(
        new Response(JSON.stringify({ version: 1 }), {
          status: 200,
          headers: {
            "content-disposition": 'attachment; filename="settings-20260102.json"',
          },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const r = await api.exportSettings();
    expect(r.data).toEqual({ version: 1 });
    expect(r.filename).toBe("settings-20260102.json");
  });
});

// ---------------- 错误路径与降级分支补测 ----------------

describe("http 错误分支", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("fetch 抛错（网络失败）→ APIError kind=network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );
    await expect(api.status()).rejects.toMatchObject({
      status: 0,
      kind: "network",
    });
  });

  it("200 但非 JSON content-type → APIError kind=non-json", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response("plain", {
            status: 200,
            headers: { "content-type": "text/plain" },
          }),
        ),
      ),
    );
    await expect(api.status()).rejects.toMatchObject({ kind: "non-json" });
  });

  it("403 改密要求在非豁免路径广播 apiForbiddenEvent", async () => {
    const handler = vi.fn();
    window.addEventListener(apiForbiddenEvent, handler);
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            new Response(
              JSON.stringify({
                code: 403,
                message:
                  "Password change required before performing this operation",
                data: null,
              }),
              { status: 403, headers: { "content-type": "application/json" } },
            ),
          ),
        ),
      );
      await expect(api.changeUsername("newname")).rejects.toMatchObject({
        status: 403,
      });
      expect(handler).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(apiForbiddenEvent, handler);
    }
  });

  it("403 改密要求在豁免路径（/user/login）不广播", async () => {
    const handler = vi.fn();
    window.addEventListener(apiForbiddenEvent, handler);
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            new Response(
              JSON.stringify({
                code: 403,
                message:
                  "Password change required before performing this operation",
                data: null,
              }),
              { status: 403, headers: { "content-type": "application/json" } },
            ),
          ),
        ),
      );
      await expect(
        api.login({ username: "u", password: "p", expire: 0 }),
      ).rejects.toBeInstanceOf(APIError);
      expect(handler).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener(apiForbiddenEvent, handler);
    }
  });

  it("403 凭标记头判定改密要求：message 文案可自由调整", async () => {
    const handler = vi.fn();
    window.addEventListener(apiForbiddenEvent, handler);
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            new Response(
              JSON.stringify({ code: 403, message: "请先修改初始密码", data: null }),
              {
                status: 403,
                headers: {
                  "content-type": "application/json",
                  "x-novaveil-error": "password_change_required",
                },
              },
            ),
          ),
        ),
      );
      await expect(api.changeUsername("newname")).rejects.toMatchObject({
        status: 403,
      });
      expect(handler).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(apiForbiddenEvent, handler);
    }
  });

  it("403 无标记头且非改密文案时不广播", async () => {
    const handler = vi.fn();
    window.addEventListener(apiForbiddenEvent, handler);
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            new Response(
              JSON.stringify({ code: 403, message: "forbidden for other reasons", data: null }),
              { status: 403, headers: { "content-type": "application/json" } },
            ),
          ),
        ),
      );
      await expect(api.changeUsername("newname")).rejects.toMatchObject({
        status: 403,
      });
      expect(handler).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener(apiForbiddenEvent, handler);
    }
  });
});

describe("导出下载错误与文件名解析", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("exportChannels 失败时抛出信封 message", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 500, message: "export failed", data: null }),
            { status: 500, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    await expect(api.exportChannels()).rejects.toMatchObject({
      status: 500,
      statusText: "export failed",
    });
  });

  it("exportChannels 解析 filename*（RFC 5987）并解码 UTF-8", async () => {
    // "UTF-8''"（RFC 5987 定界）拆开拼接，避免源码里三连单引号歧义
    const header = "attachment; filename*=UTF-8" + "''" + "channels%20%E6%B8%A0%E9%81%93.txt";
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response("dump", {
            status: 200,
            headers: { "content-disposition": header },
          }),
        ),
      ),
    );
    const r = await api.exportChannels();
    expect(r.filename).toBe("channels 渠道.txt");
  });

  it("exportSettings 响应非 JSON 抛 APIError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("not json", { status: 200 }))),
    );
    await expect(api.exportSettings()).rejects.toMatchObject({
      status: 200,
      statusText: "Invalid JSON response",
    });
  });

  it("导出 401（JWT 过期）广播 apiUnauthorizedEvent，与 http() 行为一致", async () => {
    const handler = vi.fn();
    window.addEventListener(apiUnauthorizedEvent, handler);
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            new Response(
              JSON.stringify({ code: 401, message: "unauthorized", data: null }),
              { status: 401, headers: { "content-type": "application/json" } },
            ),
          ),
        ),
      );
      await expect(api.exportChannels()).rejects.toMatchObject({ status: 401 });
      expect(handler).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(apiUnauthorizedEvent, handler);
    }
  });

  it("导出网络失败抛 kind=network 的 APIError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );
    await expect(api.exportChannels()).rejects.toMatchObject({
      status: 0,
      kind: "network",
    });
  });
});

describe("归一化函数降级分支", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("getNowVersion 非对象响应回退全零结构", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok(null))));
    await expect(api.getNowVersion()).resolves.toEqual({
      version: "",
      commit: "",
      build_time: "",
      client_ip_count: 0,
      total_requests: 0,
      error_count: 0,
      total_tokens_input: 0,
      total_tokens_output: 0,
      tokens_by_model: [],
    });
  });

  it("getNowVersion tokens_by_model 兼容 model/total_tokens 并丢弃非法项", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok({
            version: "1",
            tokens_by_model: [
              { name: "a", input: 1, output: 2 },
              { model: "b", total_tokens: 5 },
              null,
              "junk",
              { name: "", input: 9, output: 9 },
            ],
          }),
        ),
      ),
    );
    const r = await api.getNowVersion();
    expect(r.tokens_by_model).toEqual([
      { name: "a", input: 1, output: 2 },
      { name: "b", input: 5, output: 0 },
    ]);
  });

  it("lastSyncTime 把 0001-01-01 零值时间归一为空串", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(ok({ last_sync_at: "0001-01-01T00:00:00Z" })),
      ),
    );
    await expect(api.lastSyncTime()).resolves.toEqual({ last_sync_at: "" });
  });

  it("clientStats 兼容 ip/request_count 字段并丢弃无 IP 项", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            { ip: "10.0.0.1", request_count: 7, last_seen: "t" },
            { client_ip: "10.0.0.2", requests: 3 },
            null,
            { requests: 9 },
          ]),
        ),
      ),
    );
    // 后端 ClientStat 无 error_count 字段（幻影字段已从类型中移除）
    await expect(api.clientStats()).resolves.toEqual([
      { client_ip: "10.0.0.1", requests: 7, first_seen: "", last_seen: "t" },
      { client_ip: "10.0.0.2", requests: 3, first_seen: "", last_seen: "" },
    ]);
  });

  it("stopAllState 回退 stopped 布尔字段", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok({ stopped: true }))));
    await expect(api.stopAllState()).resolves.toEqual({ is_stopped: true });
  });

  it("fetchModels 表单模式：对象模型 + capabilities 过滤非法项", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            { name: "gpt-4o", capabilities: ["chat", 42, null] },
            { name: 123 },
            null,
            "raw-model",
          ]),
        ),
      ),
    );
    const models = await api.fetchModels({
      type: "openai",
      base_url: "https://x",
    });
    expect(models).toEqual([
      { name: "gpt-4o", capabilities: ["chat"] },
      { name: "raw-model", capabilities: undefined },
    ]);
  });

  it("clearGroupCooldown → POST /group/cooldown/clear/:id，缺省字段归零", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(ok({ group_id: 3 })));
    vi.stubGlobal("fetch", fetchMock);
    const r = await api.clearGroupCooldown(3);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/group/cooldown/clear/3",
      expect.objectContaining({ method: "POST" }),
    );
    expect(r).toEqual({
      group_id: 3,
      channels: 0,
      member_items: 0,
      key_cooldowns: 0,
      rate_windows: 0,
    });
  });

  it("testChannelKeys 非数组响应回空数组并过滤非法项", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            { key_id: "k1", label: "#1", ok: true, elapsed_ms: 1 },
            null,
            "junk",
          ]),
        ),
      ),
    );
    expect(await api.testChannelKeys(5, "m")).toHaveLength(1);

    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok("nope"))));
    expect(await api.testChannelKeys(5, "m")).toEqual([]);
  });

  it("testChannel 失败契约：后端以 HTTP 500 表达，拒绝时抛 APIError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 500, message: "upstream boom", data: null }),
            { status: 500, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    await expect(api.testChannel(1, "m")).rejects.toMatchObject({
      status: 500,
      statusText: "upstream boom",
    });
  });
});

describe("parseHeaderTemplates", () => {
  it("解析合法模板：过滤无名/无头非法项", () => {
    expect(
      parseHeaderTemplates(
        JSON.stringify([
          {
            name: "默认",
            headers: [
              { header_key: "X-A", header_value: "1" },
              { header_key: 1, header_value: "2" },
            ],
          },
          { name: "无头" },
          { name: "", headers: [] },
          null,
        ]),
      ),
    ).toEqual([
      { name: "默认", headers: [{ header_key: "X-A", header_value: "1" }] },
      { name: "无头", headers: [] },
    ]);
  });

  it("非法输入回退空数组", () => {
    expect(parseHeaderTemplates(undefined)).toEqual([]);
    expect(parseHeaderTemplates(null)).toEqual([]);
    expect(parseHeaderTemplates("")).toEqual([]);
    expect(parseHeaderTemplates("not json")).toEqual([]);
    expect(parseHeaderTemplates(JSON.stringify({ name: "x" }))).toEqual([]);
  });
});

describe("无 body POST 的 RequireJSON 契约", () => {
  afterEach(() => vi.unstubAllGlobals());

  function expectJsonPost(
    fetchMock: ReturnType<typeof vi.fn>,
    urlPart: string,
  ) {
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).includes(urlPart),
    );
    expect(call).toBeTruthy();
    const [, init] = call!;
    expect((init as RequestInit).method).toBe("POST");
    expect((init as RequestInit).body).toBe("{}");
    const headers = new Headers((init as RequestInit).headers);
    expect(headers.get("content-type")).toContain("application/json");
  }

  it("logout 发送 {} + application/json，避免后端 RequireJSON 415 导致假登出", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(ok(null)));
    vi.stubGlobal("fetch", fetchMock);
    await api.logout();
    expectJsonPost(fetchMock, "/user/logout");
  });

  it("clearGroupCooldown 发送 {} + application/json", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(ok(null)));
    vi.stubGlobal("fetch", fetchMock);
    await api.clearGroupCooldown(7);
    expectJsonPost(fetchMock, "/group/cooldown/clear/7");
  });
});
