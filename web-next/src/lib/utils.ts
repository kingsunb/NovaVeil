import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import type { DBDump } from "./types";

/**
 * shadcn 风格 className 合并器：clsx + tailwind-merge
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/**
 * 数字格式化：千分位 + 表格等宽
 */
export function formatNumber(n: number, opts: Intl.NumberFormatOptions = {}) {
  return new Intl.NumberFormat("zh-CN", opts).format(n);
}

/**
 * 把 Unix 秒数格式化为本地 datetime-local 输入可用的 "YYYY-MM-DDTHH:mm"。
 * datetime-local 输入不包含时区，浏览器按本地时间解析；这里用本地时间组件拼装，
 * 避免 toISOString() 产出 UTC 字符串再被 datetime-local 视为本地时间时产生时区偏移。
 */
export function formatDatetimeLocal(seconds: number): string {
  const d = new Date(seconds * 1000);
  const pad = (n: number) => n.toString().padStart(2, "0");
  return (
    d.getFullYear() +
    "-" +
    pad(d.getMonth() + 1) +
    "-" +
    pad(d.getDate()) +
    "T" +
    pad(d.getHours()) +
    ":" +
    pad(d.getMinutes())
  );
}

/**
 * 字节可读化
 */
export function formatBytes(bytes: number) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    units.length - 1,
  );
  return `${(bytes / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
}

/**
 * 相对时间（如 "3 分钟前"）
 */
export function timeAgo(input: string | number | Date) {
  const d = new Date(input);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return `${Math.floor(diff)} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`;
  return d.toLocaleDateString("zh-CN");
}

export function formatDuration(
  request: {
    status: string;
    started_at: string;
    duration?: number;
    duration_ms?: number;
  },
  now = Date.now(),
) {
  if (request.status === "running" || request.status === "committed") {
    const startedAt = new Date(request.started_at).getTime();
    if (!Number.isFinite(startedAt)) return "正在请求";
    const elapsed = Math.max(0, now - startedAt);
    return `正在请求 · ${formatElapsed(elapsed)}`;
  }
  const milliseconds =
    request.duration_ms ??
    (request.duration != null ? request.duration / 1_000_000 : 0);
  return milliseconds > 0 ? `${Math.round(milliseconds)}ms` : "—";
}

/**
 * 耗时展示的结构化形态。终态/已提交请求返回首字 + 总耗时两段，运行中请求仅返回当前总耗时。
 * 首字时点来自后端 first_token_at（首个已交付客户端事件/整响应提交时刻）。
 * 状态覆盖：
 *  - running：首字尚未到达客户端，仅返回当前总耗时。
 *  - committed：首字已交付，仍在传输；总耗时仍按「当前到请求到达」计算，
 *    首字耗时只是其中的一个阶段标记，不是总耗时的起点。
 *  - 终态（success/failed/canceled）：总耗时取定稿后 duration_ms。
 */
export type ElapsedParts =
  | { kind: "none" }
  | { kind: "running"; total: string }
  | { kind: "total"; total: string }
  | { kind: "first-total"; first: string; total: string };

export function elapsedParts(
  request: {
    status: string;
    started_at: string;
    first_token_at?: string;
    duration_ms?: number;
    duration?: number;
  },
  now = Date.now(),
): ElapsedParts {
  const startedAt = new Date(request.started_at).getTime();
  if (!Number.isFinite(startedAt)) {
    const ms =
      request.duration_ms ??
      (request.duration != null ? request.duration / 1_000_000 : 0);
    return ms > 0 ? { kind: "total", total: `${Math.round(ms)}ms` } : { kind: "none" };
  }

  // 首字耗时（毫秒）：首字时点相对请求到达的差值；未提交首字时按 0 处理。
  let firstElapsedMs = 0;
  const firstAt = request.first_token_at
    ? new Date(request.first_token_at).getTime()
    : null;
  if (firstAt != null && Number.isFinite(firstAt) && firstAt >= startedAt) {
    firstElapsedMs = firstAt - startedAt;
  }

  // 总耗时（毫秒）：
  //  - running / committed：当前到请求到达(started_at)的差值，覆盖完整请求生命周期
  //  - 终态：定稿 duration_ms
  const totalMs =
    request.status === "running" || request.status === "committed"
      ? Math.max(0, now - startedAt)
      : request.duration_ms ??
        (request.duration != null ? request.duration / 1_000_000 : 0);

  const firstStr = formatElapsed(firstElapsedMs);
  const totalStr = formatElapsed(totalMs);

  if (request.status === "running") {
    return { kind: "running", total: totalStr };
  }

  // 首字已交付时返回首字 + 总耗时；首字未到（如提交前失败）只返回纯总耗时。
  if (firstElapsedMs > 0) {
    return { kind: "first-total", first: firstStr, total: totalStr };
  }
  return totalMs > 0 ? { kind: "total", total: totalStr } : { kind: "none" };
}

/** 单行字符串形态（详情弹窗等宽裕场景使用）；表格窄列请用 elapsedParts 分两行渲染。 */
export function formatElapsedWithFirst(
  request: Parameters<typeof elapsedParts>[0],
  now = Date.now(),
) {
  const parts = elapsedParts(request, now);
  switch (parts.kind) {
    case "running":
      return `正在请求 · ${parts.total}`;
    case "first-total":
      return `首字 ${parts.first} · 总耗时 ${parts.total}`;
    case "total":
      return parts.total;
    case "none":
      return "—";
  }
}

export function formatElapsed(milliseconds: number) {
  // 毫秒范围直接按毫秒显示（如 110ms → "110ms"），避免低于 1s 被 floor 成 0s。
  if (milliseconds < 1000) return `${Math.round(milliseconds)}ms`;
  const totalSeconds = Math.floor(milliseconds / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (totalMinutes < 60) {
    return `${totalMinutes}m${seconds.toString().padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return `${hours}h${minutes.toString().padStart(2, "0")}m`;
}

/**
 * 通用防抖
 */
export function debounce<T extends (...args: unknown[]) => void>(
  fn: T,
  wait: number,
) {
  let timer: ReturnType<typeof setTimeout> | null = null;
  return (...args: Parameters<T>) => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}

/**
 * 触发浏览器下载 JSON
 */
export function downloadJson(filename: string, data: unknown) {
  downloadText(filename, JSON.stringify(data, null, 2), "application/json");
}

/** 触发浏览器下载原始文本（用于后端的纯文本导出接口）。 */
export function downloadText(
  filename: string,
  text: string,
  type = "text/plain;charset=utf-8",
) {
  const blob = new Blob([text], { type });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/**
 * 导入设置前的校验 —— 抽成纯函数便于单测
 *  - 1 MB 上限（防止 DoS）
 *  - 必须 JSON parse 成功
 *  - 顶层必须是 plain object
 *  - 所有 value 必须是 string（后端 KV 存储）
 *
 * 返回校验后的对象；失败抛 Error。
 */
export function validateSettingsImport(text: string, size: number): Record<string, string> {
  const MAX = 1024 * 1024;
  if (size > MAX) {
    throw new Error(`文件过大（${(size / 1024).toFixed(0)} KB > 1 MB）`);
  }
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch (e) {
    throw new Error("JSON 解析失败：" + (e as Error).message, { cause: e });
  }
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    throw new Error("JSON 顶层必须是对象（key → value）");
  }
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(data)) {
    if (typeof v !== "string") {
      throw new Error(`设置项 ${k} 的值必须是字符串，实际为 ${typeof v}`);
    }
    out[k] = v;
  }
  return out;
}

/**
 * 导入完整数据库备份前的校验。
 * 后端 /setting/import 接受 DBDump（不是只有 KV 的设置对象），上限与后端
 * maxDBImportBodyBytes 对齐为 128 MiB；对象数量和表结构由后端再次校验。
 * 这里只做粗校验：顶层结构、版本号、关键表字段必须是数组，避免前端发起的非
 * DBDump（手工 KV 平铺、误传 settings 列表等）被后端忽略但前端以为导入成功。
 */
export function validateDBDumpImport(
  text: string,
  size: number,
): DBDump {
  const MAX = 128 * 1024 * 1024;
  if (size > MAX) {
    throw new Error(`文件过大（${(size / 1024 / 1024).toFixed(1)} MB > 128 MB）`);
  }
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch (e) {
    throw new Error("JSON 解析失败：" + (e as Error).message, { cause: e });
  }
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    throw new Error("备份顶层必须是对象");
  }
  const dump = data as DBDump;
  if (dump.version !== undefined && typeof dump.version !== "number") {
    throw new Error("备份 version 必须是数字");
  }
  const arrayFields: Array<keyof DBDump> = [
    "channels",
    "channel_models",
    "groups",
    "group_items",
    "api_keys",
    "settings",
  ];
  for (const key of arrayFields) {
    const value = (dump as unknown as Record<string, unknown>)[key];
    if (value === undefined || value === null) continue;
    if (!Array.isArray(value)) {
      throw new Error(`备份 ${key} 必须是数组（如果不是请删除该字段后重试）`);
    }
  }
  return dump;
}

/**
 * 通用表单字段校验 —— 抽成纯函数便于单测
 */

export interface FieldRule {
  /** 必填 */
  required?: boolean;
  /** trim 后最大长度 */
  maxLen?: number;
  /** trim 后最小长度 */
  minLen?: number;
  /** 正则匹配（pattern 不匹配时报 pattern 错误） */
  pattern?: { re: RegExp; message: string };
  /** 自定义校验 */
  validate?: (v: string) => string | null;
}

export function validateField(value: string, rule: FieldRule): string | null {
  const v = value.trim();
  if (rule.required && v.length === 0) return "必填";
  if (v.length === 0) return null; // 非必填且为空：通过
  if (rule.minLen && v.length < rule.minLen) return `至少 ${rule.minLen} 个字符`;
  if (rule.maxLen && v.length > rule.maxLen) return `不能超过 ${rule.maxLen} 个字符`;
  if (rule.pattern && !rule.pattern.re.test(v)) return rule.pattern.message;
  if (rule.validate) {
    const msg = rule.validate(v);
    if (msg) return msg;
  }
  return null;
}

/** 资源名（渠道/分组/密钥）通用规则 */
export const NAME_RULE: FieldRule = {
  required: true,
  minLen: 1,
  maxLen: 64,
  pattern: {
    re: /^[\w\u4e00-\u9fa5][\w\u4e00-\u9fa5\-. ]*$/,
    message: "只能包含字母、数字、中文、空格、-、_、.，且必须以字母/数字/中文开头",
  },
};

/** URL 校验 */
export const URL_RULE: FieldRule = {
  required: true,
  pattern: {
    re: /^https?:\/\/.+/i,
    message: "必须以 http:// 或 https:// 开头",
  },
};

/** 模型名校验 */
export const MODEL_RULE: FieldRule = {
  required: true,
  maxLen: 128,
  pattern: {
    re: /^[a-zA-Z0-9][a-zA-Z0-9._\-/:]*$/,
    message: "模型名格式非法",
  },
};
