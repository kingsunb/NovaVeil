import type {
  APIKeyCreated,
  APIKeyRequest,
  APIKeySummary,
  Channel,
  ChannelKey,
  ChannelKeyTestResult,
  ChannelTestResult,
  ChannelUpdateRequest,
  ChannelImportResult,
  ClientStats,
  DBDump,
  BuildInfo,
  GroupClearCooldownResult,
  LastSyncTime,
  DBImportResult,
  ErrorLog,
  FailedRuntime,
  FetchedModel,
  Group,
  GroupTestResult,
  GroupUpdateRequest,
  HeaderTemplate,
  MaskConfig,
  MaskRuleMeta,
  MaskTestResult,
  NowVersion,
  ProxyTestResult,
  SettingItem,
  StopAllResult,
  StopAllState,
  TokenTrendPoint,
  TokenTrendRange,
  UsageDetail,
  UsageHeatmapPoint,
  UserLoginRequest,
  UserStatus,
} from "./types";
import type {
  EvalApplyProResult,
  EvalEnqueueResult,
  EvalHistoryPage,
  EvalHistoryQuery,
  EvalQueueList,
  EvalQueueTask,
  EvalRankContent,
  EvalRankList,
  EvalRecord,
  EvalRemovedResult,
  EvalStatsList,
} from "./model-eval";
import { openSSE } from "./sse";

/**
 * 后端 API 客户端
 *  - 统一拆 `{code, message, data}` 信封
 *  - 错误抛 APIError(status, message, body)
 *  - credentials: "include" 走 JWT cookie
 *
 * 路径对齐 internal/server/handlers/* 的 NewRoute。
 */

const BASE = "/api/v1";

/**
 * 后端信封的运行时形状是 {code, message, data}，但 code 从不被校验——成败纯按
 * HTTP 状态码判断（2xx 成功路径只取 data，非 2xx 错误路径只取 message），类型
 * 不声明未读取的字段。
 */
interface Envelope<T> {
  message: string;
  data: T;
}

async function requestInit(init: RequestInit): Promise<RequestInit> {
  return {
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...(init.headers || {}),
    },
    ...init,
  };
}

/**
 * isJsonContentType 判断响应头是否声明为 JSON, 用于决定解析策略。
 */
function isJsonContentType(contentType: string | null): boolean {
  return contentType !== null && contentType.toLowerCase().includes("application/json");
}

/**
 * isInstanceOf 替代 instanceof Web API：避免测试环境（jsdom）缺少某个类时崩。
 */
function isInstanceOf<T>(value: unknown, constructorName: string): value is T {
  const ctor = (globalThis as unknown as Record<string, unknown>)[constructorName];
  return (
    typeof ctor === "function" &&
    value instanceof (ctor as abstract new (...args: never[]) => T)
  );
}

/**
 * shouldSendJsonHeader：FormData / URLSearchParams / Blob / ArrayBuffer
 * 由 fetch 自行处理 boundary，无需 Content-Type。
 */
function shouldSendJsonHeader(body: unknown): boolean {
  if (body === undefined || body === null) return false;
  if (isInstanceOf<FormData>(body, "FormData")) return false;
  if (isInstanceOf<URLSearchParams>(body, "URLSearchParams")) return false;
  if (isInstanceOf<Blob>(body, "Blob")) return false;
  if (isInstanceOf<ArrayBuffer>(body, "ArrayBuffer")) return false;
  return true;
}

function toRequestBody(body: unknown): BodyInit | undefined {
  if (body === undefined) return undefined;
  if (
    isInstanceOf<FormData>(body, "FormData") ||
    isInstanceOf<URLSearchParams>(body, "URLSearchParams") ||
    isInstanceOf<Blob>(body, "Blob") ||
    isInstanceOf<ArrayBuffer>(body, "ArrayBuffer")
  ) {
    return body as BodyInit;
  }
  // 已 stringify 的字符串原样返回，避免双重序列化导致 JSON 解析失败。
  if (typeof body === "string") return body;
  return JSON.stringify(body);
}

/**
 * parseErrorMessage 从 JSON 响应体中尽力提取 message 字段，失败时回退到 raw 文本。
 */
function parseErrorMessage(raw: string, fallback: string): string {
  if (raw.length === 0) return fallback;
  try {
    const parsed = JSON.parse(raw) as { message?: unknown };
    if (parsed && typeof parsed.message === "string" && parsed.message.length > 0) {
      return parsed.message;
    }
  } catch {
    // 不是 JSON，使用原始文本作为错误信息更直观。
  }
  return raw.length < 200 ? raw : fallback;
}

/**
 * 认证自身使用的端点，403 时不触发强制改密引导（避免登录流程反复弹 toast）。
 */
const forbiddenGuideExemptPaths = new Set<string>([
  // 注意：这里存的是 http() 收到的相对 path（BASE = /api/v1 在 fetch 时才拼接），
  // 之前写成 /api/v1/... 全路径导致 has() 永远不命中，登录 403 会误弹改密引导。
  "/user/login",
  "/user/status",
  "/user/change-password",
]);

/**
 * 强制改密的判定: 后端 403 会携带机器可读标记头（X-NovaVeil-Error:
 * password_change_required，见 resp.ErrorMustChangePassword），文案今后可自由
 * 调整；message 精确匹配仅作为对旧后端（dev 模式新前端对旧构建）的兜底。
 */
function isPasswordChangeRequired(res: Response, message: string): boolean {
  return (
    res.headers.get("x-novaveil-error") === "password_change_required" ||
    message === "Password change required before performing this operation"
  );
}

/**
 * 统一认证失败广播：401 → 全局未授权事件（App 层登出跳登录）；403 改密要求 →
 * 引导 toast。http() 与 rawDownload（导出等不走信封的端点）共用，
 * 保证导出遇到过期 JWT 时同样登出，而不是停留在已失效页面。
 */
function broadcastAuthFailure(
  status: number,
  message: string,
  path: string,
  dispatchUnauthorized = true,
  passwordChangeRequired = false,
): void {
  if (typeof window === "undefined") return;
  if (status === 401 && dispatchUnauthorized) {
    window.dispatchEvent(new Event(apiUnauthorizedEvent));
  }
  if (status === 403 && passwordChangeRequired && !forbiddenGuideExemptPaths.has(path)) {
    window.dispatchEvent(new Event(apiForbiddenEvent));
  }
}

/**
 * broadcastStreamAuthFailure 供 Chat 等不使用 http() 信封的裸 fetch 路径(SSE)复用:
 * 与 http() 相同的 401/403 广播语义, 避免聊天页请求在 JWT 过期后停留原地而不是
 * 登出跳登录(审计 FE-01)。
 */
export function broadcastStreamAuthFailure(res: Response, message: string): void {
  broadcastAuthFailure(
    res.status,
    message,
    "/chat/completions",
    true,
    isPasswordChangeRequired(res, message),
  );
}

async function http<T>(
  path: string,
  init: {
    method?: string;
    body?: unknown;
    dispatchUnauthorized?: boolean;
    signal?: AbortSignal;
  } = {},
): Promise<T> {
  const headers = new Headers();
  if (shouldSendJsonHeader(init.body)) {
    headers.set("Content-Type", "application/json");
  }

  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      method: init.method ?? "GET",
      headers,
      body: toRequestBody(init.body),
      credentials: "include",
      cache: "no-store",
      signal: init.signal,
    });
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause);
    throw new APIError(0, message || "Network request failed", undefined, "network");
  }

  if (init.signal?.aborted) {
    throw new APIError(0, "Request aborted", undefined, "network");
  }

  // 204 / 205：纯成功响应，无 body
  if (res.status === 204 || res.status === 205) {
    return undefined as T;
  }

  let raw: string;
  try {
    raw = await res.text();
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause);
    throw new APIError(0, message || "Failed to read response", undefined, "network");
  }

  if (!res.ok) {
    const errorMessage = parseErrorMessage(raw, `Request failed: ${res.status}`);
    broadcastAuthFailure(
      res.status,
      errorMessage,
      path,
      init.dispatchUnauthorized !== false,
      isPasswordChangeRequired(res, errorMessage),
    );
    // 从错误响应体里取 data 透传给 APIError body（失败则 null）
    let errorBody: unknown = null;
    try {
      const envErr = JSON.parse(raw) as { data?: unknown } | null;
      errorBody = envErr?.data ?? null;
    } catch {
      // body 不是 JSON，保持 null
    }
    throw new APIError(res.status, errorMessage, errorBody, "http");
  }

  // 空 body 的 2xx：按约定的 T 返回 undefined（不改外层调用方代码）
  if (raw.length === 0) {
    return undefined as T;
  }

  const contentType = res.headers.get("content-type");
  const isJson = isJsonContentType(contentType);

  if (!isJson) {
    throw new APIError(0, `Expected JSON response, got: ${contentType ?? "unknown"}`, null, "non-json");
  }

  try {
    const parsed = JSON.parse(raw) as { data?: T } | null;
    return (parsed?.data as T) ?? (undefined as T);
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause);
    throw new APIError(0, `Invalid JSON response: ${message}`, null, "parse");
  }
}

/**
 * filenameFromContentDisposition 从下载响应头解析服务端建议的文件名
 * （/channel/export、/setting/export 会带 Content-Disposition）。
 * filename*（RFC 5987）优先；解析不出时返回 undefined，由调用方回退本地命名。
 */
function filenameFromContentDisposition(res: Response): string | undefined {
  const header = res.headers.get("content-disposition");
  if (!header) return undefined;
  const extended = /filename\*=(?:UTF-8'')?([^;]+)/i.exec(header);
  if (extended?.[1]) {
    const raw = extended[1].trim().replace(/^"|"$/g, "");
    try {
      return decodeURIComponent(raw) || undefined;
    } catch {
      return raw || undefined;
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header);
  return plain?.[1]?.trim() || undefined;
}

/** 后端 /channel/export 与 /setting/export 是原始下载响应，不走 {code,data} 信封。 */
async function rawDownload(
  path: string,
  init: RequestInit = {},
): Promise<{ text: string; filename?: string }> {
  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, { ...(await requestInit(init)), cache: "no-store" });
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause);
    throw new APIError(0, message || "Network request failed", undefined, "network");
  }
  if (init.signal?.aborted) {
    throw new APIError(0, "Request aborted", undefined, "network");
  }
  const text = await res.text();
  if (!res.ok) {
    let message = res.statusText || "Request failed";
    try {
      const env = JSON.parse(text) as Partial<Envelope<unknown>>;
      message = env.message || message;
    } catch {
      // 保留原始文本作为错误消息
    }
    // 导出端点与 http() 同一套认证失败广播：401 过期要登出，403 改密要引导。
    broadcastAuthFailure(res.status, message, path, true, isPasswordChangeRequired(res, message));
    throw new APIError(res.status, message, text);
  }
  return { text, filename: filenameFromContentDisposition(res) };
}

async function rawDownloadJson<T>(
  path: string,
  init: RequestInit = {},
): Promise<{ data: T; filename?: string }> {
  const { text, filename } = await rawDownload(path, init);
  try {
    return { data: JSON.parse(text) as T, filename };
  } catch {
    throw new APIError(200, "Invalid JSON response", text);
  }
}

/**
 * ApiErrorKind 区分网络失败、HTTP 状态码与成功响应解析失败。
 * 借鉴自 NovaVeil/web：让 ErrorBoundary / 重连逻辑能精准判断是 chunk 丢失
 * 还是真的 4xx/5xx，从而决定清缓存刷新 vs 单纯重试。
 */
export type ApiErrorKind = "network" | "http" | "non-json" | "parse";

/**
 * 全局事件名 —— 后端明确返回 401 时 dispatch apiUnauthorizedEvent（App 层
 * 监听后登出并跳登录页）；返回 403 且提示 "Password change required" 时
 * dispatch apiForbiddenEvent（App 层监听后 toast 引导改密）。
 */
export const apiUnauthorizedEvent = "api:unauthorized";
export const apiForbiddenEvent = "api:must-change-password";

export class APIError extends Error {
  constructor(
    public status: number,
    public statusText: string,
    public body: unknown,
    public kind: ApiErrorKind = "http",
  ) {
    super(statusText || `${status} request failed`);
    this.name = "APIError";
  }
}

/**
 * /channel/test 成功响应的严格归一化。后端契约（relay.ChannelTestResult）只有
 * model/content/elapsed_ms/prompt_tokens/completion_tokens 五个字段；失败不走
 * 200+ok:false 而是 HTTP 5xx + 信封 message，由调用方 catch 统一处理——
 * 曾经的 ok/latency_ms/error 兼容分支在 200 路径上恒走成功臂，已删除。
 */
function normalizeChannelTestResult(raw: unknown): ChannelTestResult {
  const value =
    raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  return {
    model: typeof value.model === "string" ? value.model : "",
    content: typeof value.content === "string" ? value.content : "",
    latency_ms: typeof value.elapsed_ms === "number" ? value.elapsed_ms : 0,
    prompt_tokens:
      typeof value.prompt_tokens === "number" ? value.prompt_tokens : 0,
    completion_tokens:
      typeof value.completion_tokens === "number"
        ? value.completion_tokens
        : 0,
  };
}

function normalizeBuildInfo(raw: unknown): BuildInfo {
  const value = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  return {
    version: typeof value.version === "string" ? value.version : "",
    commit: typeof value.commit === "string" ? value.commit : "",
    build_time: typeof value.build_time === "string" ? value.build_time : "",
  };
}

function normalizeNowVersion(raw: unknown): NowVersion {
  if (!raw || typeof raw !== "object") {
    return {
      version: "",
      commit: "",
      build_time: "",
      client_ip_count: 0,
      total_requests: 0,
      error_count: 0,
      total_tokens_input: 0,
      total_tokens_output: 0,
      tokens_by_model: [],
    };
  }
  const value = raw as Record<string, unknown>;
  const tokens = Array.isArray(value.tokens_by_model)
    ? value.tokens_by_model.flatMap((item) => {
        if (!item || typeof item !== "object") return [];
        const token = item as Record<string, unknown>;
        const name =
          typeof token.name === "string"
            ? token.name
            : typeof token.model === "string"
              ? token.model
              : "";
        if (!name) return [];
        const input =
          typeof token.input === "number"
            ? token.input
            : typeof token.total_tokens === "number"
              ? token.total_tokens
              : 0;
        const output = typeof token.output === "number" ? token.output : 0;
        return [{ name, input, output }];
      })
    : [];
  return {
    version: typeof value.version === "string" ? value.version : "",
    commit: typeof value.commit === "string" ? value.commit : "",
    build_time: typeof value.build_time === "string" ? value.build_time : "",
    client_ip_count:
      typeof value.client_ip_count === "number" ? value.client_ip_count : 0,
    total_requests:
      typeof value.total_requests === "number" ? value.total_requests : 0,
    error_count:
      typeof value.error_count === "number" ? value.error_count : 0,
    total_tokens_input:
      typeof value.total_tokens_input === "number" ? value.total_tokens_input : 0,
    total_tokens_output:
      typeof value.total_tokens_output === "number" ? value.total_tokens_output : 0,
    tokens_by_model: tokens,
  };
}

function normalizeLastSyncTime(raw: unknown): LastSyncTime {
  const value =
    typeof raw === "string"
      ? raw
      : raw && typeof raw === "object" && typeof (raw as { last_sync_at?: unknown }).last_sync_at === "string"
        ? (raw as { last_sync_at: string }).last_sync_at
        : "";
  return { last_sync_at: value.startsWith("0001-01-01") ? "" : value };
}

function normalizeClientStats(raw: unknown): ClientStats[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    if (!item || typeof item !== "object") return [];
    const value = item as Record<string, unknown>;
    const clientIP =
      typeof value.client_ip === "string"
        ? value.client_ip
        : typeof value.ip === "string"
          ? value.ip
          : "";
    if (!clientIP) return [];
    return [
      {
        client_ip: clientIP,
        requests:
          typeof value.requests === "number"
            ? value.requests
            : typeof value.request_count === "number"
              ? value.request_count
              : 0,
        first_seen: typeof value.first_seen === "string" ? value.first_seen : "",
        last_seen: typeof value.last_seen === "string" ? value.last_seen : "",
      },
    ];
  });
}

function normalizeStopAllResult(raw: unknown): StopAllResult {
  const value = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  return {
    stopped: typeof value.stopped === "number" ? value.stopped : 0,
    is_stopped: value.is_stopped === true,
  };
}

function normalizeGroupClearCooldownResult(
  raw: unknown,
): GroupClearCooldownResult {
  const value = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  return {
    group_id: typeof value.group_id === "number" ? value.group_id : 0,
    channels: typeof value.channels === "number" ? value.channels : 0,
    member_items:
      typeof value.member_items === "number" ? value.member_items : 0,
    key_cooldowns:
      typeof value.key_cooldowns === "number" ? value.key_cooldowns : 0,
    rate_windows:
      typeof value.rate_windows === "number" ? value.rate_windows : 0,
  };
}

function normalizeFetchedModels(raw: unknown): FetchedModel[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    if (typeof item === "string") return [{ name: item }];
    if (
      item &&
      typeof item === "object" &&
      typeof (item as { name?: unknown }).name === "string"
    ) {
      const model = item as { name: string; capabilities?: unknown };
      return [
        {
          name: model.name,
          capabilities: Array.isArray(model.capabilities)
            ? model.capabilities.filter((v): v is string => typeof v === "string")
            : undefined,
        },
      ];
    }
    return [];
  });
}

function normalizeStopAllState(raw: unknown): StopAllState {
  const value =
    raw && typeof raw === "object"
      ? (raw as { stopped?: unknown; is_stopped?: unknown })
      : {};
  return {
    is_stopped:
      typeof value.is_stopped === "boolean"
        ? value.is_stopped
        : typeof value.stopped === "boolean"
          ? value.stopped
          : false,
  };
}

/**
 * normalizeTokenTrend 归一化 /update/token-trends 响应：
 * 兼容 {points:[...]} 包装与裸数组两种形态，严格过滤非法项（t 非有限时间戳、
 * in/out 非有限非负数、类型错误、重复/越界时间），非法项丢弃，全部合法时保持
 * 原序返回；缺省/异常返回空数组由图表渲染「暂无数据」。
 * 防御上游异常 payload：NaN/±Infinity、负 token 均不进入渲染层，避免 NaN 坐标。
 */
export function normalizeTokenTrend(raw: unknown): TokenTrendPoint[] {
  const list = Array.isArray(raw)
    ? raw
    : raw && typeof raw === "object" && Array.isArray((raw as { points?: unknown }).points)
      ? (raw as { points: unknown[] }).points
      : [];
  return list
    .filter((item) => isTokenTrendPoint(item))
    // 排序保证 t 单调递增：时间序列依赖顺序渲染；重复时间点去重保留首条
    .filter((item, idx, arr) => arr.findIndex((x) => x.t === item.t) === idx)
    .map((item) => ({ t: item.t, in: item.in, out: item.out }))
    // 兜底：防御个别项在过滤后仍可能未满足的边界（如时间戳非正）
    .filter((p) => p.t > 0 && p.in >= 0 && p.out >= 0)
    .slice(-130); // 防御性截断: 保留最新点(最旧→最新), 避免异常长序列撑爆渲染
}

function isTokenTrendPoint(item: unknown): item is TokenTrendPoint {
  if (!item || typeof item !== "object") return false;
  const v = item as Record<string, unknown>;
  return (
    typeof v.t === "number" &&
    typeof v.in === "number" &&
    typeof v.out === "number" &&
    // 非有限数（NaN/±Infinity）直接排除
    Number.isFinite(v.t) &&
    Number.isFinite(v.in) &&
    Number.isFinite(v.out) &&
    v.t > 0
  );
}

/**
 * normalizeChannelOne 单渠道归一化：补齐后端 omitempty 字段的前端默认值。
 * 列表与 create/update 接口共用，保证保存响应写回缓存时与列表数据同形。
 */
export function normalizeChannelOne(raw: unknown): Channel | null {
  if (!raw || typeof raw !== "object") return null;
  const channel = raw as Record<string, unknown>;
  return {
    ...channel,
    key: typeof channel.key === "string" ? channel.key : "",
    is_free: typeof channel.is_free === "boolean" ? channel.is_free : false,
    builtin: typeof channel.builtin === "boolean" ? channel.builtin : false,
    fixed_reply:
      typeof channel.fixed_reply === "string" ? channel.fixed_reply : "",
    keys: Array.isArray(channel.keys) ? channel.keys : [],
    models: Array.isArray(channel.models) ? channel.models : [],
    tags: Array.isArray(channel.tags) ? channel.tags : [],
    custom_header: Array.isArray(channel.custom_header)
      ? channel.custom_header
      : [],
    model_limits:
      channel.model_limits && typeof channel.model_limits === "object"
        ? channel.model_limits
        : {},
    rate_limit_rpm:
      typeof channel.rate_limit_rpm === "number" ? channel.rate_limit_rpm : 0,
    max_concurrent:
      typeof channel.max_concurrent === "number" ? channel.max_concurrent : 0,
    sort: typeof channel.sort === "number" ? channel.sort : 0,
    opencode_compat:
      typeof channel.opencode_compat === "boolean"
        ? channel.opencode_compat
        : false,
    pass_through_body_enabled:
      typeof channel.pass_through_body_enabled === "boolean"
        ? channel.pass_through_body_enabled
        : false,
  } as unknown as Channel;
}

function normalizeChannels(raw: unknown): Channel[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    const normalized = normalizeChannelOne(item);
    return normalized ? [normalized] : [];
  });
}

/**
 * normalizeGroupOne 单分组归一化：items 补 client_uid（编辑器草稿的稳定标识）。
 * 列表与 create/update/active 接口共用，保证保存响应写回缓存时与列表数据同形。
 */
export function normalizeGroupOne(raw: unknown): Group | null {
  if (!raw || typeof raw !== "object") return null;
  const group = raw as Record<string, unknown>;
  const items = Array.isArray(group.items) ? group.items : [];
  return {
    ...group,
    items: items.flatMap((rawItem, index) => {
      if (!rawItem || typeof rawItem !== "object") return [];
      const groupItem = rawItem as Record<string, unknown>;
      const id = typeof groupItem.id === "number" ? groupItem.id : 0;
      return [
        {
          ...groupItem,
          // 仅供编辑器本地识别；saved:id 稳定，new:* 避免多个 id=0 碰撞。
          client_uid:
            typeof groupItem.client_uid === "string"
              ? groupItem.client_uid
              : id > 0
                ? `saved:${id}`
                : `server:${index}`,
        },
      ];
    }),
  } as unknown as Group;
}

function normalizeGroups(raw: unknown): Group[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    const normalized = normalizeGroupOne(item);
    return normalized ? [normalized] : [];
  });
}

/** fetch-model 的完整表单形态（未保存的新渠道 / 编辑态改了地址时使用）。 */
interface FetchModelPayload {
  id?: number;
  type: string;
  base_url: string;
  key?: string;
  keys?: ChannelKey[];
  proxy?: boolean;
  channel_proxy?: string | null;
  match_regex?: string | null;
  custom_header?: Channel["custom_header"];
}

/**
 * parseHeaderTemplates 解析 header_templates 设置项的 JSON。
 * 非法 JSON / 非数组 / 字段缺失时回退空数组，与后端校验的宽松读取保持一致；
 * 序列化（保存）由调用方负责先行校验。
 */
export function parseHeaderTemplates(
  value: string | undefined | null,
): HeaderTemplate[] {
  if (!value) return [];
  try {
    const parsed = JSON.parse(value) as HeaderTemplate[];
    if (!Array.isArray(parsed)) return [];
    return parsed.flatMap((tpl): HeaderTemplate[] => {
      if (!tpl || typeof tpl.name !== "string" || !tpl.name) return [];
      const headers = Array.isArray(tpl.headers)
        ? tpl.headers.flatMap((h) =>
            h &&
            typeof h.header_key === "string" &&
            typeof h.header_value === "string"
              ? [{ header_key: h.header_key, header_value: h.header_value }]
              : [],
          )
        : [];
      return [{ name: tpl.name, headers }];
    });
  } catch {
    return [];
  }
}

export const api = {
  // ----- 用户 -----
  login: (body: UserLoginRequest) =>
    http<UserStatus>("/user/login", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  // logout/clearGroupCooldown 是无业务 body 的 POST，但后端 RequireJSON 对
  // 非 GET/DELETE 强制 Content-Type（validate.go），缺头会被 415 拒绝：
  // logout 的 Set-Cookie 清除将永远执行不到（假登出）。空对象占位满足契约。
  logout: () => http<null>("/user/logout", { method: "POST", body: {} }),
  status: () => http<UserStatus>("/user/status"),
  changePassword: (oldPassword: string, newPassword: string) =>
    http<null>("/user/change-password", {
      method: "POST",
      body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
    }),
  changeUsername: (newUsername: string) =>
    http<null>("/user/change-username", {
      method: "POST",
      body: JSON.stringify({ new_username: newUsername }),
    }),

  // ----- 总览 -----
  /**
   * 仪表盘总览 KPI。传入 range 时按该时间窗口聚合四项 KPI（与趋势图档位联动）；
   * 省略 range 时后端默认 forever（全量）。range 同时作为 queryKey 的一部分，
   * 切换档位会触发重新拉取。
   */
  getNowVersion: async (range?: string) => {
    const query = range ? `?range=${encodeURIComponent(range)}` : "";
    const raw = await http<unknown>(`/stats/now-version${query}`);
    return normalizeNowVersion(raw);
  },
  /**
   * 当前二进制构建元信息（version/commit/build_time）。轻量端点：只读
   * ldflags 常量、无统计聚合，供前端版本看门狗轮询判断前后端是否错位。
   */
  getBuildInfo: async () => {
    const raw = await http<unknown>(`/stats/build-info`);
    return normalizeBuildInfo(raw);
  },
  /** Token 用量趋势（按时间分桶的真实时序，24h/7d/30d）。 */
  getTokenTrends: async (range: TokenTrendRange) => {
    const raw = await http<unknown>(
      `/stats/token-trends?range=${encodeURIComponent(range)}`,
    );
    return normalizeTokenTrend(raw);
  },
  /** 详细指标（input/output/reasoning/cached/cost/duration）。 */
  getUsageDetail: async (range: TokenTrendRange) => {
    const raw = await http<unknown>(
      `/stats/usage-detail?range=${encodeURIComponent(range)}`,
    ) as { detail?: UsageDetail; available?: boolean };
    return raw?.detail ?? {
      input_tokens: 0, output_tokens: 0, reasoning_tokens: 0,
      cached_tokens: 0, cost: 0, duration_ms: 0, request_count: 0,
    };
  },
  /** 热力图数据（每日 token/cost/count 汇总）。 */
  getUsageHeatmap: async (days = 365) => {
    const raw = await http<unknown>(
      `/stats/usage-heatmap?days=${days}`,
    ) as { points?: UsageHeatmapPoint[]; available?: boolean };
    return raw?.points ?? [];
  },

  // ----- 渠道 -----
  listChannels: async () => {
    const raw = await http<unknown>("/channel/list");
    return normalizeChannels(raw);
  },
  // 保存接口返回后端刷新后的完整实体（已归一化）：调用方直接写回 React Query
  // 缓存即可即时更新界面，无需等列表 refetch 二次往返（弱网/长链路下延迟明显）。
  createChannel: async (body: Omit<Channel, "id">) =>
    normalizeChannelOne(
      await http<unknown>("/channel/create", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    ) as Channel,
  updateChannel: async (req: ChannelUpdateRequest) =>
    normalizeChannelOne(
      await http<unknown>("/channel/update", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    ) as Channel,
  enableChannel: (id: number, enabled: boolean) =>
    http<null>("/channel/enable", {
      method: "POST",
      body: JSON.stringify({ id, enabled }),
    }),
  deleteChannel: (id: number) =>
    http<null>(`/channel/delete/${id}`, { method: "DELETE" }),
  // keyId 省略时后端选第一把健康 Key；传入已保存密钥的 id 可指定用哪把密钥
  // 测试（并绕过冷却），供「按密钥测试」选择器使用。
  testChannel: async (id: number, model?: string, message?: string, keyId?: string) => {
    const body: Record<string, unknown> = { id };
    if (model) body.model = model;
    if (message) body.message = message;
    if (keyId) body.key_id = keyId;
    const raw = await http<unknown>("/channel/test", {
      method: "POST",
      body: JSON.stringify(body),
    });
    return normalizeChannelTestResult(raw);
  },
  // 逐密钥测试：后端对每把密钥各发一条测试消息（并发 4、单 Key 30s 超时），
  // 按配置顺序返回逐 Key 有效性结果。
  testChannelKeys: async (id: number, model?: string, message?: string) => {
    const body: Record<string, unknown> = { id };
    if (model) body.model = model;
    if (message) body.message = message;
    const raw = await http<unknown>("/channel/test_keys", {
      method: "POST",
      body: JSON.stringify(body),
    });
    if (!Array.isArray(raw)) return [];
    return raw.filter(
      (item): item is ChannelKeyTestResult =>
        !!item && typeof item === "object" && typeof (item as ChannelKeyTestResult).key_id === "string",
    );
  },
  fetchModels: async (input: number | FetchModelPayload) => {
    // 传渠道 ID：后端用已存配置（含密钥）请求；传完整表单 payload（未保存的
    // 新渠道 / 编辑态改了地址）时按表单值请求，密钥为空回退已存渠道凭据，
    // 避免「不改密钥就必须先点眼睛拿明文」的死锁。
    const body =
      typeof input === "number"
        ? { id: input }
        : {
            id: input.id ?? 0,
            type: input.type,
            base_url: input.base_url,
            key: input.key ?? "",
            keys: input.keys ?? [],
            proxy: input.proxy ?? false,
            channel_proxy: input.channel_proxy ?? "",
            match_regex: input.match_regex ?? "",
            custom_header: input.custom_header ?? [],
          };
    const raw = await http<unknown>("/channel/fetch-model", {
      method: "POST",
      body: JSON.stringify(body),
    });
    return normalizeFetchedModels(raw);
  },
  /** 渠道密钥明文：管理列表只回掩码，这里按需取回全部明文（仅管理员会话）。 */
  getChannelKeys: async (id: number) => {
    const raw = await http<unknown>(`/channel/keys/${id}`, {
      method: "POST",
      body: "{}",
    });
    if (!Array.isArray(raw)) return [];
    return raw.flatMap((item): ChannelKey[] => {
      if (!item || typeof item !== "object") return [];
      const v = item as Record<string, unknown>;
      return [
        {
          id: typeof v.id === "string" ? v.id : "",
          key: typeof v.key === "string" ? v.key : "",
          remark: typeof v.remark === "string" ? v.remark : "",
        },
      ];
    });
  },
  // 后端语义是全局同步（body 可为空）。
  syncAllChannels: () => http<null>("/channel/sync", { method: "POST" }),
  exportChannels: () => rawDownload("/channel/export", { method: "POST", body: "{}" }),
  /** 渠道导入：用导出文件内容批量创建渠道，返回成功/失败数量及原因。 */
  importChannels: (text: string) =>
    http<ChannelImportResult>("/channel/import", {
      method: "POST",
      body: JSON.stringify({ text }),
    }),
  lastSyncTime: async () => {
    const raw = await http<unknown>("/channel/last-sync-time");
    return normalizeLastSyncTime(raw);
  },

  // ----- 分组 -----
  listGroups: async () => {
    const raw = await http<unknown>("/group/list");
    return normalizeGroups(raw);
  },
  /** 按分组测试：对分组内每个成员（渠道+模型）按真实路由逻辑发起测试。 */
  testGroup: async (id: number, message?: string) => {
    const body: Record<string, unknown> = { id };
    if (message) body.message = message;
    const raw = await http<unknown>("/group/test", {
      method: "POST",
      body: JSON.stringify(body),
    });
    if (!Array.isArray(raw)) return [];
    return raw.filter(
      (item): item is GroupTestResult =>
        !!item &&
        typeof item === "object" &&
        typeof (item as GroupTestResult).channel_name === "string",
    );
  },
  // 同渠道：保存/设当前成员的响应即最新分组实体，归一化后供调用方写回缓存。
  createGroup: async (body: Omit<Group, "id">) =>
    normalizeGroupOne(
      await http<unknown>("/group/create", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    ) as Group,
  updateGroup: async (req: GroupUpdateRequest) =>
    normalizeGroupOne(
      await http<unknown>("/group/update", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    ) as Group,
  setActiveGroupItem: async (id: number, itemId: number | null) =>
    normalizeGroupOne(
      await http<unknown>(`/group/active/${id}`, {
        method: "POST",
        body: JSON.stringify({ item_id: itemId ?? 0 }),
      }),
    ) as Group,
  deleteGroup: (id: number) =>
    http<null>(`/group/delete/${id}`, { method: "DELETE" }),
  clearGroupCooldown: async (id: number) => {
    const raw = await http<unknown>(`/group/cooldown/clear/${id}`, {
      method: "POST",
      body: {},
    });
    return normalizeGroupClearCooldownResult(raw);
  },

  // ----- 密钥 -----
  listKeys: () => http<APIKeySummary[]>("/apikey/list"),
  /** API 密钥明文：编辑表单「查看密钥」按需取回（仅管理员会话）。 */
  getAPIKeySecret: async (id: number) => {
    const raw = await http<unknown>(`/apikey/secret/${id}`, {
      method: "POST",
      body: "{}",
    });
    const v = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
    return typeof v.api_key === "string" ? v.api_key : "";
  },
  createKey: (body: Omit<APIKeyRequest, "id">) =>
    http<APIKeyCreated>("/apikey/create", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateKey: (body: APIKeyRequest) =>
    http<APIKeySummary>("/apikey/update", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  deleteKey: (id: number) =>
    http<null>(`/apikey/delete/${id}`, { method: "DELETE" }),

  // ----- 模型评估 -----
  runModelEval: (channelModelId: number, signal?: AbortSignal) =>
    http<EvalRecord>("/model-eval/run", {
      method: "POST",
      body: { channel_model_id: channelModelId },
      signal,
    }),
  listModelEvals: (filters: EvalHistoryQuery = {}, signal?: AbortSignal) => {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) {
      if (value !== undefined && value !== "") query.set(key, String(value));
    }
    return http<EvalHistoryPage>(`/model-eval/list?${query}`, { signal });
  },
  getModelEval: (id: number, signal?: AbortSignal) =>
    http<EvalRecord>(`/model-eval/${id}`, { signal }),
  listEvalStats: (signal?: AbortSignal) =>
    http<EvalStatsList>("/model-eval/stats", { signal }),
  listEvalRanks: (signal?: AbortSignal) =>
    http<EvalRankList>("/model-eval/rank/list", { signal }),
  getEvalRankContent: (id: number, signal?: AbortSignal) =>
    http<EvalRankContent>(`/model-eval/rank/content/${id}`, { signal }),
  moveEvalRank: (id: number, direction: -1 | 1) =>
    http<EvalRankList>("/model-eval/rank/move", {
      method: "POST",
      body: { id, direction },
    }),
  setEvalRankPosition: (id: number, position: number) =>
    http<EvalRankList>("/model-eval/rank/move", {
      method: "POST",
      body: { id, position },
    }),
  removeEvalRank: (id: number) =>
    http<EvalRankList>("/model-eval/rank/remove", {
      method: "POST",
      body: { id },
    }),
  addEvalRankFromHistory: (evalId: number) =>
    http<EvalRankList>("/model-eval/rank/from-history", {
      method: "POST",
      body: { eval_id: evalId },
    }),
  applyProGroup: () =>
    http<EvalApplyProResult>("/model-eval/rank/apply-pro", {
      method: "POST",
      body: {},
    }),
  enqueueEvals: (channelModelIds: number[]) =>
    http<EvalEnqueueResult>("/model-eval/queue/enqueue", {
      method: "POST",
      body: { channel_model_ids: channelModelIds },
    }),
  listEvalQueue: (signal?: AbortSignal) =>
    http<EvalQueueList>("/model-eval/queue/list", { signal }),
  moveUpEvalQueue: (id: number) =>
    http<EvalQueueList>("/model-eval/queue/move-up", {
      method: "POST",
      body: { id },
    }),
  stopEvalQueue: (id: number) =>
    http<EvalQueueList>("/model-eval/queue/stop", {
      method: "POST",
      body: { id },
    }),
  clearEvalQueue: () =>
    http<EvalRemovedResult>("/model-eval/queue/clear", {
      method: "POST",
      body: {},
    }),
  streamEvalQueue: (onMessage: (items: EvalQueueTask[]) => void) =>
    openSSE<EvalQueueTask[]>("/api/v1/model-eval/queue/stream", {
      eventName: "queue",
      onMessage,
    }),
  clearEvalFailures: () =>
    http<EvalRemovedResult>("/model-eval/history/clear-failures", {
      method: "POST",
      body: {},
    }),

  // ----- 日志 -----
  listFailures: (limit = 50, className?: string) => {
    const q = new URLSearchParams();
    q.set("limit", String(limit));
    if (className) q.set("class", className);
    return http<FailedRuntime[]>(`/log/failures?${q.toString()}`);
  },
  listErrorLogs: (limit = 50, className?: string) => {
    const q = new URLSearchParams();
    q.set("limit", String(limit));
    if (className) q.set("class", className);
    return http<ErrorLog[]>(`/log/errors?${q.toString()}`);
  },
  clearLogs: () => http<null>("/log/clear", { method: "DELETE" }),
  clearErrorLogs: () => http<null>("/log/errors", { method: "DELETE" }),
  stopAll: async () => {
    const raw = await http<unknown>("/log/stop-all", { method: "POST" });
    return normalizeStopAllResult(raw);
  },
  resumeAll: async () => {
    const raw = await http<unknown>("/log/resume-all", { method: "POST" });
    return normalizeStopAllState(raw);
  },
  stopAllState: async () => {
    const raw = await http<{ stopped?: unknown; is_stopped?: unknown }>(
      "/log/stop-all-state",
    );
    return normalizeStopAllState(raw);
  },
  clientStats: async () => {
    const raw = await http<unknown>("/log/client-stats");
    return normalizeClientStats(raw);
  },
  // 后端 resp.Success 把原始字符串直接塞进 data，不是 {body:string} 包装
  getRequestBody: (id: number) => http<string>(`/log/${id}/request-body`),
  getResponseBody: (id: number) => http<string>(`/log/${id}/response-body`),
  // 后端成功时 data 是字符串 "request stopped"；请求不存在/已结束时返回 404，
  // 而不是 {stopped:false}。/log 组无 RequireJSON，与其余日志端点一样无 body。
  stopRequest: (id: number) =>
    http<string>(`/log/${id}/stop-request`, {
      method: "POST",
    }),
  interruptRound: (id: number, round: number) =>
    http<{ interrupted: boolean; reason?: string }>(
      `/log/${id}/${round}/stop`,
      { method: "POST" },
    ),

  // ----- 设置 -----
  listSettings: () => http<SettingItem[]>("/setting/list"),
  getSetting: (key: string) =>
    http<SettingItem>(`/setting/get?key=${encodeURIComponent(key)}`),
  setSetting: (key: string, value: string) =>
    http<SettingItem>("/setting/set", {
      method: "POST",
      body: JSON.stringify({ key, value }),
    }),
  testProxy: (url: string) =>
    http<ProxyTestResult>("/setting/proxy/test", {
      method: "POST",
      body: JSON.stringify({ url }),
    }),
  exportSettings: () =>
    rawDownloadJson<DBDump>("/setting/export", { method: "POST", body: "{}" }),
  importSettings: (data: DBDump) =>
    http<DBImportResult>("/setting/import", {
      method: "POST",
      body: JSON.stringify(data),
    }),

  // ----- 对话审计/训练留存 -----
  getConversationStats: () =>
    http<{
      enabled: boolean;
      dir: string;
      file_count: number;
      total_bytes: number;
      max_bytes: number;
      retention_days: number;
      pending_bytes: number;
      dropped_total: number;
      oldest_day: string;
      newest_day: string;
    }>("/conversation/stats"),
  clearConversations: () =>
    http<{ removed: number }>("/conversation/clear", { method: "DELETE" }),

  // ----- 脱敏 -----
  /** 读取脱敏全局配置（设置项缺失时后端返回全关默认）。 */
  getMaskConfig: () => http<MaskConfig>("/mask/config"),
  /** 更新脱敏全局配置（全局开关、规则开关、自定义拦截词），热生效。 */
  putMaskConfig: (cfg: MaskConfig) =>
    http<MaskConfig>("/mask/config", {
      method: "PUT",
      body: JSON.stringify(cfg),
    }),
  /** 内置规则元信息（标签、说明、默认开关），供管理台渲染规则列表。 */
  getMaskRules: async () => {
    const raw = await http<unknown>("/mask/rules");
    if (!Array.isArray(raw)) return [];
    return raw.filter(
      (item): item is MaskRuleMeta =>
        !!item &&
        typeof item === "object" &&
        typeof (item as MaskRuleMeta).label === "string",
    );
  },
  /** 脱敏预览测试：输入文本 → 返回脱敏结果 + 命中明细，不入库不转发。 */
  testMask: (text: string) =>
    http<MaskTestResult>("/mask/test", {
      method: "POST",
      body: JSON.stringify({ text }),
    }),
};
