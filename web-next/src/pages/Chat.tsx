import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Eraser, Send, Square, User, Bot, MessageSquare, AlertTriangle } from "lucide-react";

import { api, apiUnauthorizedEvent, broadcastStreamAuthFailure } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { EmptyState } from "@/components/ui/empty-state";
import { cn } from "@/lib/utils";

interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
  error?: string;
}

const CHAT_SESSION_KEY = "novaveil:chat:current";
const CHAT_MASK_SESSION_KEY = "novaveil:chat:mask-session";

interface ChatSession {
  groupName: string | null;
  messages: ChatMessage[];
}

function loadSession(): ChatSession | null {
  try {
    const raw = localStorage.getItem(CHAT_SESSION_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<ChatSession> & {
      target?: { kind?: string; groupName?: string };
    };
    const groupName =
      parsed.groupName ??
      (parsed.target?.kind === "group" ? parsed.target.groupName : null) ??
      null;
    return {
      groupName,
      messages: (parsed.messages ?? []).filter((m) => !m.streaming),
    };
  } catch {
    return null;
  }
}

function saveSession(session: ChatSession): void {
  try {
    if (session.messages.some((m) => m.streaming)) return;
    localStorage.setItem(CHAT_SESSION_KEY, JSON.stringify(session));
  } catch {
    // ignore
  }
}

function clearSession(): void {
  try {
    localStorage.removeItem(CHAT_SESSION_KEY);
    // H-02 残余: 新会话必须同时清除 X-Session-Id 对应的 mask-session 粘合 ID,
    // 否则下周会话仍带着旧 sticky key 继续粘到同一上游成员。
    localStorage.removeItem(CHAT_MASK_SESSION_KEY);
  } catch {
    // ignore
  }
}

function maskSessionId(): string {
  try {
    const existing = localStorage.getItem(CHAT_MASK_SESSION_KEY);
    if (existing) return existing;
    const id =
      typeof crypto !== "undefined" && crypto.randomUUID
        ? crypto.randomUUID()
        : `chat-${Date.now()}`;
    localStorage.setItem(CHAT_MASK_SESSION_KEY, id);
    return id;
  } catch {
    return `chat-${Date.now()}`;
  }
}

async function streamChatCompletion(
  model: string,
  messages: { role: string; content: string }[],
  sessionId: string,
  onDelta: (delta: string) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch("/api/v1/chat/completions", {
    method: "POST",
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      "X-Session-Id": sessionId,
    },
    body: JSON.stringify({ model, messages, stream: true }),
    signal,
  });

  if (!res.ok) {
    let msg = `请求失败 (${res.status})`;
    try {
      const text = await res.text();
      const parsed = JSON.parse(text) as { message?: string; error?: { message?: string } };
      if (parsed.message) msg = parsed.message;
      else if (parsed.error?.message) msg = parsed.error.message;
    } catch {
      // keep default
    }
    // 与 api.http() 走同一套认证失败广播: 401 全局登出、403 强制改密引导(审计 FE-01)。
    broadcastStreamAuthFailure(res, msg);
    throw new Error(msg);
  }

  const contentType = res.headers.get("content-type") || "";
  if (!contentType.includes("text/event-stream")) {
    const data = (await res.json()) as {
      choices?: { message?: { content?: string } }[];
    };
    const content = data.choices?.[0]?.message?.content;
    if (content) onDelta(content);
    return;
  }

  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() ?? "";
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed.startsWith("data:")) continue;
      const payload = trimmed.slice(5).trim();
      if (payload === "[DONE]") return;
      try {
        const parsed = JSON.parse(payload) as {
          choices?: { delta?: { content?: string } }[];
        };
        const delta = parsed.choices?.[0]?.delta?.content;
        if (delta) onDelta(delta);
      } catch {
        // skip malformed
      }
    }
  }
}

export default function ChatPage() {
  const [initialSession] = useState(loadSession);
  const [groupName, setGroupName] = useState<string | null>(initialSession?.groupName ?? null);
  const [messages, setMessages] = useState<ChatMessage[]>(initialSession?.messages ?? []);
  const [input, setInput] = useState("");
  const [streaming, setStreaming] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const sessionIdRef = useRef(maskSessionId());

  const { data: groups } = useQuery({ queryKey: ["groups"], queryFn: api.listGroups });
  const groupOptions = useMemo(
    () => (groups ?? []).map((g) => ({ value: g.name, label: g.name })),
    [groups],
  );

  const hasTarget = !!groupName;
  const canSend = !!input.trim() && !streaming && hasTarget;

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    // Chat 裸 fetch 收到 401 时由 broadcastStreamAuthFailure 广播 apiUnauthorizedEvent;
    // 这里同步清理本地会话, 避免重新登录后接着展示过期会话与旧粘合 ID(H-02 残余)。
    const onUnauthorized = () => {
      clearSession();
      sessionIdRef.current = maskSessionId();
      setMessages([]);
    };
    window.addEventListener(apiUnauthorizedEvent, onUnauthorized);
    return () => window.removeEventListener(apiUnauthorizedEvent, onUnauthorized);
  }, []);

  useEffect(() => {
    if (groupName && groupOptions.length > 0 && !groupOptions.some((g) => g.value === groupName)) {
      setGroupName(null);
    }
  }, [groupOptions, groupName]);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    if (!streaming) {
      saveSession({ groupName, messages });
    }
  }, [groupName, messages, streaming]);

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text || !groupName || streaming) return;

    const userMsg: ChatMessage = { role: "user", content: text };
    const assistantMsg: ChatMessage = { role: "assistant", content: "", streaming: true };
    const history = [...messages, userMsg];
    setMessages([...history, assistantMsg]);
    setInput("");
    setStreaming(true);

    const controller = new AbortController();
    abortRef.current = controller;

    try {
      await streamChatCompletion(
        groupName,
        history.map((m) => ({ role: m.role, content: m.content })),
        sessionIdRef.current,
        (delta) => {
          setMessages((prev) => {
            const next = [...prev];
            const last = next[next.length - 1];
            if (last?.role === "assistant") {
              next[next.length - 1] = { ...last, content: last.content + delta };
            }
            return next;
          });
        },
        controller.signal,
      );
      setMessages((prev) => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last?.role === "assistant") {
          next[next.length - 1] = { ...last, streaming: false };
        }
        return next;
      });
    } catch (e) {
      if (controller.signal.aborted) return;
      const msg = (e as Error).message;
      setMessages((prev) => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last?.role === "assistant") {
          if (last.content === "") {
            next.pop();
          } else {
            next[next.length - 1] = { ...last, streaming: false };
          }
        }
        return [...next, { role: "assistant", content: "", error: msg }];
      });
    } finally {
      setStreaming(false);
      abortRef.current = null;
    }
  }, [input, groupName, streaming, messages]);

  const handleStop = useCallback(() => {
    abortRef.current?.abort();
    setStreaming(false);
    setMessages((prev) => {
      const next = [...prev];
      const last = next[next.length - 1];
      if (last?.role === "assistant" && last.streaming) {
        next[next.length - 1] = { ...last, streaming: false };
      }
      return next;
    });
  }, []);

  const handleClear = useCallback(() => {
    setMessages([]);
    clearSession();
    // 清掉粘合 ID 后立即生成新 ID: 本周对话用全新 X-Session-Id, 不继承旧粘合。
    sessionIdRef.current = maskSessionId();
  }, []);

  return (
    <div className="flex h-full flex-col gap-3">
      <Card className="shrink-0 px-4 py-3">
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex min-w-0 items-center gap-2">
            <label className="shrink-0 text-xs font-medium text-ink-muted">分组</label>
            <Select
              aria-label="选择对话分组"
              className="h-8 min-w-48 flex-1 text-sm"
              value={groupName ?? ""}
              onChange={(e) => setGroupName(e.target.value || null)}
            >
              <option value="">选择分组…</option>
              {groupOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          </div>
          {messages.length > 0 && (
            <Button
              variant="ghost"
              size="sm"
              className="gap-1.5 text-ink-muted"
              onClick={handleClear}
            >
              <Eraser className="h-3.5 w-3.5" aria-hidden />
              新会话
            </Button>
          )}
        </div>
      </Card>

      <div ref={scrollRef} className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        {groupOptions.length === 0 ? (
          <EmptyState
            className="m-auto"
            icon={<MessageSquare className="h-5 w-5" aria-hidden />}
            title="没有可用的分组"
            hint="请先在「渠道」和「分组」页面创建。对话走管理员会话，不再使用 API 密钥。"
          />
        ) : messages.length === 0 ? (
          <EmptyState
            className="m-auto"
            icon={<MessageSquare className="h-5 w-5" aria-hidden />}
            title={hasTarget ? "输入消息开始对话" : "选择一个分组开始对话"}
          />
        ) : (
          <div className="mx-auto w-full max-w-3xl space-y-3">
            {messages.map((msg, i) => (
              <MessageBubble key={i} msg={msg} />
            ))}
          </div>
        )}
      </div>

      <Card className="shrink-0 px-4 py-3">
        <div className="flex items-end gap-2">
          <textarea
            aria-label="消息输入"
            rows={3}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                e.preventDefault();
                void handleSend();
              }
            }}
            placeholder={!hasTarget ? "请先选择分组…" : "输入消息, ⌘/Ctrl+Enter 发送…"}
            disabled={!hasTarget || streaming}
            className={cn(
              "min-h-0 flex-1 resize-none rounded-control border border-border/60 bg-card/60 px-3 py-2 text-sm backdrop-blur-sm",
              "placeholder:text-ink-subtle transition-all duration-150",
              "focus:bg-card/80 focus:outline-none focus:ring-[3px] focus:ring-primary/15 focus:border-primary/40",
              "disabled:cursor-not-allowed disabled:opacity-50",
            )}
          />
          {streaming ? (
            <Button variant="secondary" size="sm" className="gap-1.5" onClick={handleStop}>
              <Square className="h-3.5 w-3.5" aria-hidden />
              停止
            </Button>
          ) : (
            <Button
              variant="primary"
              size="sm"
              className="gap-1.5"
              disabled={!canSend}
              onClick={() => void handleSend()}
            >
              <Send className="h-3.5 w-3.5" aria-hidden />
              发送
            </Button>
          )}
        </div>
      </Card>
    </div>
  );
}

function MessageBubble({ msg }: { msg: ChatMessage }) {
  if (msg.error) {
    return (
      <div className="flex justify-center">
        <div className="flex max-w-[80%] items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
          <span className="break-words">{msg.error}</span>
        </div>
      </div>
    );
  }

  const isUser = msg.role === "user";
  return (
    <div className={cn("flex gap-2", isUser ? "justify-end" : "justify-start")}>
      {!isUser && (
        <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary">
          <Bot className="h-4 w-4" aria-hidden />
        </div>
      )}
      <div
        className={cn(
          "max-w-[75%] whitespace-pre-wrap break-words px-3.5 py-2 text-sm leading-relaxed",
          isUser
            ? "rounded-[18px] rounded-br-md bg-primary text-primary-foreground shadow-apple-sm"
            : "rounded-[18px] rounded-bl-md border border-border/60 bg-card/80 text-ink",
        )}
      >
        {msg.content}
        {msg.streaming && (
          <span className="ml-1 inline-block h-3.5 w-1.5 animate-pulse bg-ink-muted/60 align-middle" />
        )}
      </div>
      {isUser && (
        <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-ink/10 text-ink-muted">
          <User className="h-4 w-4" aria-hidden />
        </div>
      )}
    </div>
  );
}
