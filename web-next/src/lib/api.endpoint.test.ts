import { afterEach, describe, expect, it, vi } from "vitest";
import { api, APIError } from "./api";

const ok = (data: unknown) =>
  new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });

function mockJson() {
  return vi.fn(() => Promise.resolve(ok(null) as Response));
}

function urlOf(call: unknown[] | undefined): string {
  return (call?.[0] as string) ?? "";
}
function methodOf(call: unknown[] | undefined): string {
  const init = call?.[1] as { method?: string } | undefined;
  return init?.method ?? "";
}
function bodyOf(call: unknown[] | undefined): string {
  const init = call?.[1] as { body?: unknown } | undefined;
  const b = init?.body;
  return typeof b === "string" ? b : "";
}
function jsonBodyOf(call: unknown[] | undefined): Record<string, unknown> {
  return JSON.parse(bodyOf(call)) as Record<string, unknown>;
}

afterEach(() => vi.unstubAllGlobals());

describe("api 关键 endpoint 路径", () => {
  it("listChannels → GET /channel/list", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.listChannels();
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/channel/list");
  });

  it("getNowVersion 归一化后端 name/input/output 用量", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(
        ok({
          version: "dev",
          tokens_by_model: [{ name: "gpt-4o", input: 4, output: 6 }],
        }),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    await expect(api.getNowVersion()).resolves.toMatchObject({
      version: "dev",
      tokens_by_model: [{ name: "gpt-4o", input: 4, output: 6 }],
    });
  });

  it("lastSyncTime 兼容后端裸时间字符串与对象", async () => {
    const fetch = vi.fn(() => Promise.resolve(ok("2025-01-02T03:04:05Z")));
    vi.stubGlobal("fetch", fetch);
    await expect(api.lastSyncTime()).resolves.toEqual({
      last_sync_at: "2025-01-02T03:04:05Z",
    });
  });

  it("clientStats 归一化后端 ip/request_count 字段", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(
        ok([
          {
            ip: "203.0.113.5",
            request_count: 12,
            first_seen: "2025-01-01",
            last_seen: "2025-01-02",
          },
        ]),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    // 后端 ClientStat 无错误数字段；first_seen 原样透传
    await expect(api.clientStats()).resolves.toEqual([
      {
        client_ip: "203.0.113.5",
        requests: 12,
        first_seen: "2025-01-01",
        last_seen: "2025-01-02",
      },
    ]);
  });

  it("createChannel → POST /channel/create", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.createChannel({
      name: "x",
      type: "openai",
      enabled: true,
      base_url: "https://x",
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
      rate_limit_rpm: 0,
      max_concurrent: 0,
    });
    expect(methodOf(fetch.mock.calls[0])).toBe("POST");
    expect(jsonBodyOf(fetch.mock.calls[0]).name).toBe("x");
  });

  it("enableChannel 传 id+enabled", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.enableChannel(7, true);
    expect(jsonBodyOf(fetch.mock.calls[0])).toEqual({ id: 7, enabled: true });
  });

  it("deleteChannel → DELETE", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(new Response(null, { status: 204, headers: { "content-type": "application/json" } })),
    );
    vi.stubGlobal("fetch", fetch);
    await api.deleteChannel(99);
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/channel/delete/99");
    expect(methodOf(fetch.mock.calls[0])).toBe("DELETE");
  });

  it("fetchModels → POST /channel/fetch-model with id，并归一化 string[]", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(ok(["gpt-4o", "claude-3.5-sonnet"])),
    );
    vi.stubGlobal("fetch", fetch);
    const models = await api.fetchModels(5);
    expect(models).toEqual([
      { name: "gpt-4o" },
      { name: "claude-3.5-sonnet" },
    ]);
    expect(jsonBodyOf(fetch.mock.calls[0])).toEqual({ id: 5 });
  });

  it("testChannel 归一化后端 elapsed_ms 响应", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(
        ok({
          model: "gpt-4o",
          content: "pong",
          elapsed_ms: 230,
          prompt_tokens: 1,
          completion_tokens: 2,
        }),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const result = await api.testChannel(5, "gpt-4o");
    expect(result).toMatchObject({
      model: "gpt-4o",
      content: "pong",
      latency_ms: 230,
      prompt_tokens: 1,
      completion_tokens: 2,
    });
    expect(jsonBodyOf(fetch.mock.calls[0])).toEqual({ id: 5, model: "gpt-4o" });
  });

  it("stopAllState 归一化后端 is_stopped 字段", async () => {
    const fetch = vi.fn(() => Promise.resolve(ok({ is_stopped: true })));
    vi.stubGlobal("fetch", fetch);
    await expect(api.stopAllState()).resolves.toEqual({ is_stopped: true });
  });

  it("createGroup / updateGroup / setActiveGroupItem 走正确 URL", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.createGroup({
      name: "g",
      mode: "manual",
      active_item_id: 0,
      relay_config: {} as never,
      items: [],
    });
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/group/create");
    await api.updateGroup({
      id: 3,
      items_to_add: [],
      items_to_update: [],
      items_to_delete: [],
    });
    expect(urlOf(fetch.mock.calls[1])).toBe("/api/v1/group/update");
    await api.setActiveGroupItem(3, 5);
    expect(urlOf(fetch.mock.calls[2])).toBe("/api/v1/group/active/3");
    expect(jsonBodyOf(fetch.mock.calls[2])).toEqual({ item_id: 5 });
    await api.setActiveGroupItem(3, null);
    expect(jsonBodyOf(fetch.mock.calls[3])).toEqual({ item_id: 0 });
  });

  it("createKey → POST /apikey/create", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            code: 200,
            message: "success",
            data: { id: 1, name: "k", api_key: "sk-NEW" },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const r = await api.createKey({
      name: "k",
      api_key: "",
      enabled: true,
      expire_at: 0,
      supported_models: "",
      max_concurrent: 0,
      rate_limit_rpm: 0,
    });
    expect(r.api_key).toBe("sk-NEW");
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/apikey/create");
  });

  it("updateKey → POST /apikey/update", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.updateKey({
      id: 1,
      name: "k",
      api_key: "",
      enabled: true,
      expire_at: 0,
      supported_models: "",
      max_concurrent: 5,
      rate_limit_rpm: 60,
    });
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/apikey/update");
    expect(methodOf(fetch.mock.calls[0])).toBe("POST");
  });

  it("deleteKey → DELETE", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(new Response(null, { status: 204, headers: { "content-type": "application/json" } })),
    );
    vi.stubGlobal("fetch", fetch);
    await api.deleteKey(7);
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/apikey/delete/7");
  });

  it("listErrorLogs 带 / 不带 class 过滤", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.listErrorLogs(50, "upstream_5xx");
    const url = urlOf(fetch.mock.calls[0]);
    expect(url).toContain("/api/v1/log/errors?");
    expect(url).toContain("limit=50");
    expect(url).toContain("class=upstream_5xx");
    await api.listErrorLogs(50);
    const url2 = urlOf(fetch.mock.calls[1]);
    expect(url2).toContain("limit=50");
    expect(url2).not.toContain("class=");
  });

  it("listFailures 带 / 不带 class 过滤", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.listFailures(20, "upstream_5xx");
    const url = urlOf(fetch.mock.calls[0]);
    expect(url).toContain("limit=20");
    expect(url).toContain("class=upstream_5xx");
    await api.listFailures(10);
    const url2 = urlOf(fetch.mock.calls[1]);
    expect(url2).toContain("limit=10");
    expect(url2).not.toContain("class=");
  });

  it("getRequestBody / getResponseBody → GET /log/:id/{request,response}-body", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(ok('{"foo":"bar"}')),
    );
    vi.stubGlobal("fetch", fetch);
    const req = await api.getRequestBody(11);
    expect(req).toBe('{"foo":"bar"}');
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/11/request-body");
    const res = await api.getResponseBody(11);
    expect(res).toBe('{"foo":"bar"}');
    expect(urlOf(fetch.mock.calls[1])).toBe("/api/v1/log/11/response-body");
  });

  it("stopRequest → POST /log/:id/stop-request，data 是字符串；需带 {} body 过 RequireJSON", async () => {
    const fetch = vi.fn(() => Promise.resolve(ok("request stopped")));
    vi.stubGlobal("fetch", fetch);
    const result = await api.stopRequest(42);
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/42/stop-request");
    expect(result).toBe("request stopped");
  });

  it("interruptRound → POST /log/:id/:round/stop", async () => {
    const fetch = vi.fn(() =>
      Promise.resolve(ok({ interrupted: false, reason: "round finished" })),
    );
    vi.stubGlobal("fetch", fetch);
    const result = await api.interruptRound(7, 3);
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/7/3/stop");
    expect(result.interrupted).toBe(false);
  });

  it("setSetting → POST /setting/set with key+value", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.setSetting("error_retention_days", "7");
    expect(jsonBodyOf(fetch.mock.calls[0])).toEqual({
      key: "error_retention_days",
      value: "7",
    });
  });

  it("exportSettings / importSettings 走正确方法", async () => {
    const dump = { version: 1, settings: [] };
    const fetch = vi.fn((url: string) =>
      Promise.resolve(
        url.includes("/setting/export")
          ? new Response(JSON.stringify(dump), {
              status: 200,
              headers: { "content-type": "application/json" },
            })
          : ok({ rows_affected: {} }),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    await api.exportSettings();
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/setting/export");
    expect(methodOf(fetch.mock.calls[0])).toBe("POST");
    await api.importSettings(dump);
    expect(urlOf(fetch.mock.calls[1])).toBe("/api/v1/setting/import");
    expect(methodOf(fetch.mock.calls[1])).toBe("POST");
  });

  it("listClientStats → GET /log/client-stats", async () => {
    const fetch = mockJson();
    vi.stubGlobal("fetch", fetch);
    await api.clientStats();
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/client-stats");
  });

  it("clearLogs / clearErrorLogs → DELETE", async () => {
    const fetch = vi.fn(() => Promise.resolve(ok(null) as Response));
    vi.stubGlobal("fetch", fetch);
    await api.clearLogs();
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/clear");
    expect(methodOf(fetch.mock.calls[0])).toBe("DELETE");
    await api.clearErrorLogs();
    expect(urlOf(fetch.mock.calls[1])).toBe("/api/v1/log/errors");
    expect(methodOf(fetch.mock.calls[1])).toBe("DELETE");
  });

  it("stopAll / resumeAll → POST", async () => {
    const fetch = vi.fn(() => Promise.resolve(ok(null) as Response));
    vi.stubGlobal("fetch", fetch);
    await api.stopAll();
    expect(urlOf(fetch.mock.calls[0])).toBe("/api/v1/log/stop-all");
    expect(methodOf(fetch.mock.calls[0])).toBe("POST");
    await api.resumeAll();
    expect(urlOf(fetch.mock.calls[1])).toBe("/api/v1/log/resume-all");
  });

  it("429 错误抛 APIError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({ code: 429, message: "rate limited" }),
            { status: 429, headers: { "content-type": "application/json" } },
          ),
        ),
      ),
    );
    await expect(api.status()).rejects.toBeInstanceOf(APIError);
  });

  it("204 走 no-content 分支", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(null, { status: 204, headers: { "content-type": "application/json" } }))),
    );
    const r = await api.clearLogs();
    expect(r).toBeUndefined();
  });
});
