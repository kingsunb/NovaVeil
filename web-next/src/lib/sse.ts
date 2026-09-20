/**
 * SSE (Server-Sent Events) 工具
 *  - 自动重连：指数退避，1s → 2s → 4s → 8s，最大 30s
 *  - 关闭 hook：调用 close() 后不再重连
 *  - 解析器：每条 message 抛给 onMessage
 *
 * 后端走 gin-contrib/sse（上游 v1.1.2），事件格式：
 *   event: <name>\n
 *   data:<json>\n
 *   \n
 * 冒号后无空格；浏览器 EventSource 兼容冒号后可选空格。
 */

import { api } from "./api";

export interface SSEOptions<T = unknown> {
  /** 后端基础 URL，默认 "" 表示用同源 */
  base?: string;
  /** 事件名过滤；undefined 表示接收所有 */
  eventName?: string;
  onMessage: (data: T, raw: string) => void;
  onError?: (err: Event) => void;
  onOpen?: () => void;
}

export interface SSEHandle {
  close: () => void;
}

/** 断连后认证探活的最小间隔：避免每次退避重试都打一次 /user/status。 */
const AUTH_PROBE_INTERVAL = 30_000;

export function openSSE<T = unknown>(
  url: string,
  opts: SSEOptions<T>,
): SSEHandle {
  let es: EventSource | null = null;
  let backoff = 1000;
  let closed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastAuthProbe = 0;

  // EventSource 不暴露 HTTP 状态码，断连无法区分网络抖动与 JWT 过期。
  // 断连时节流探活一次 /user/status：若会话已失效，http() 的 401 广播会
  // 统一登出，避免业务页在死会话里无限退避重试、用户停留在失效页面。
  function probeAuthOnce() {
    if (closed) return;
    const now = Date.now();
    if (now - lastAuthProbe < AUTH_PROBE_INTERVAL) return;
    lastAuthProbe = now;
    // 其余错误（网络/5xx）静默吞掉，不影响 SSE 自身的退避重连。
    void api.status().catch(() => {});
  }

  function connect() {
    if (closed) return;
    es = new EventSource(url, { withCredentials: true });

    es.onopen = () => {
      backoff = 1000;
      opts.onOpen?.();
    };

    const handler = (e: MessageEvent) => {
      let data: T;
      try {
        data = JSON.parse(e.data) as T;
      } catch (err) {
        // 非 JSON 仍回传原文
        if (typeof console !== "undefined")
          console.warn("[sse] message parse failed:", err);
        data = e.data as unknown as T;
      }
      // 回调属于页面业务代码；业务回调抛错不应破坏 SSE 监听器或后续消息。
      try {
        opts.onMessage(data, e.data);
      } catch (err) {
        if (typeof console !== "undefined")
          console.error("[sse] onMessage failed:", err);
      }
    };

    if (opts.eventName) {
      es.addEventListener(opts.eventName, handler as EventListener);
    } else {
      es.onmessage = handler;
    }

    es.onerror = (err) => {
      opts.onError?.(err);
      es?.close();
      es = null;
      if (closed) return;
      probeAuthOnce();
      timer = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 30_000);
    };
  }

  connect();

  return {
    close() {
      closed = true;
      if (timer) clearTimeout(timer);
      es?.close();
    },
  };
}
