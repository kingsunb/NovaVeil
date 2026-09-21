import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, APIError } from "./api";

const ok = <T,>(data: T) =>
  new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });

/** 批量覆盖 api.ts 中未被其他测试覆盖的端点函数，提升 branch 覆盖率。 */
describe("api 端点覆盖", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok({}))));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("设置端点", async () => {
    await api.listSettings();
    await api.getSetting("key");
    await api.setSetting("key", "val");
    await api.testProxy("http://proxy:8080");
    expect(vi.mocked(fetch)).toHaveBeenCalled();
  });

  it("对话审计端点", async () => {
    await api.getConversationStats();
    await api.clearConversations();
    expect(vi.mocked(fetch)).toHaveBeenCalled();
  });

  it("脱敏端点", async () => {
    await api.getMaskConfig();
    await api.putMaskConfig({
      enabled: false,
      builtin_rule_switch: {},
      custom_terms: [],
    });
    expect(vi.mocked(fetch)).toHaveBeenCalled();
  });

  it("getMaskRules 非数组返回空", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(ok(null))),
    );
    const result = await api.getMaskRules();
    expect(result).toEqual([]);
  });

  it("getMaskRules 过滤非法项", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            { label: "电话", description: "", default_enabled: true },
            null,
            "invalid",
            42,
          ]),
        ),
      ),
    );
    const result = await api.getMaskRules();
    expect(result).toHaveLength(1);
    expect(result[0].label).toBe("电话");
  });

  it("importSettings", async () => {
    await api.importSettings({
      version: 3,
      channels: [],
      groups: [],
      settings: [],
    });
    expect(vi.mocked(fetch)).toHaveBeenCalled();
  });

  it("deleteGroup", async () => {
    await api.deleteGroup(42);
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      expect.stringContaining("/group/delete/42"),
      expect.any(Object),
    );
  });

  it("testMask", async () => {
    await api.testMask("敏感内容");
    expect(vi.mocked(fetch)).toHaveBeenCalled();
  });
});

describe("api.getAPIKeySecret", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("返回 api_key 字符串", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(ok({ api_key: "sk-123" }))),
    );
    const result = await api.getAPIKeySecret(1);
    expect(result).toBe("sk-123");
  });

  it("raw 非 object 时返回空串", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(ok("not-an-object"))),
    );
    const result = await api.getAPIKeySecret(1);
    expect(result).toBe("");
  });

  it("api_key 非字符串时返回空串", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(ok({ api_key: 123 }))),
    );
    const result = await api.getAPIKeySecret(1);
    expect(result).toBe("");
  });
});

describe("api.getChannelProxy", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("POST /channel/proxy/:id 取回明文，非字符串回空", async () => {
    const fetchMock = vi.fn((url: string, _init?: RequestInit) => {
      if (String(url).includes("/channel/proxy/7")) {
        return Promise.resolve(ok({ channel_proxy: "http://user:secret@10.0.0.1:7890" }));
      }
      return Promise.resolve(ok({ channel_proxy: 1 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api.getChannelProxy(7)).resolves.toBe(
      "http://user:secret@10.0.0.1:7890",
    );
    expect(String(fetchMock.mock.calls[0][0])).toContain("/channel/proxy/7");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST" });
    await expect(api.getChannelProxy(8)).resolves.toBe("");
  });
});

describe("api.getChannelKeys", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("非数组返回空", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok(null))));
    expect(await api.getChannelKeys(1)).toEqual([]);
  });

  it("过滤非法项并补默认值", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          ok([
            { id: "k1", key: "sk-1", remark: "prod" },
            null,
            "invalid",
            { id: 123, key: 456 },
          ]),
        ),
      ),
    );
    const result = await api.getChannelKeys(1);
    expect(result).toHaveLength(2);
    expect(result[0]).toEqual({ id: "k1", key: "sk-1", remark: "prod" });
    expect(result[1]).toEqual({ id: "", key: "", remark: "" });
  });
});

describe("api 杂项端点", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(ok({}))));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("syncAllChannels / importChannels / changeUsername", async () => {
    await api.syncAllChannels();
    await api.importChannels("csv,data");
    await api.changeUsername("newname");
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(3);
  });
});

describe("api http 内部分支", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("空 body 2xx 返回 undefined", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(new Response("", { status: 204 })),
      ),
    );
    const result = await api.listSettings();
    expect(result).toBeUndefined();
  });
});
