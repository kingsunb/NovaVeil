import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  Activity,
  ArrowDownToLine,
  ArrowRight,
  Brain,
  Clock,
  Cpu,
  Loader2,
  Pause,
  Play,
  Square,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import { api, APIError } from "@/lib/api";
import type {
  AttemptRecord,
  ErrorLog,
  GroupItem,
  GroupRouteState,
  RequestState,
} from "@/lib/types";
import { Card } from "@/components/ui/card";
import { Pill } from "@/components/ui/pill";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { EmptyState } from "@/components/ui/empty-state";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { FormattedBody } from "@/components/ui/formatted-body";
import { MaskMatches } from "@/components/logs/MaskMatches";
import { cn, formatNumber, formatElapsed, formatElapsedWithFirst, elapsedParts } from "@/lib/utils";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { openSSE } from "@/lib/sse";
import {
  formatCountdown,
  remainingSeconds,
  useGroupRuntime,
  useNow,
} from "./useGroupRuntime";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// 命中上游缓存时, 缓存读取的输入 token 数嵌套在后端 llm.Usage 的
// prompt_tokens_details.cached_tokens 下 (RequestState.usage 类型未声明该嵌套字段,
// 此处用宽松结构类型读取; 缺省或 0 表示未命中缓存, 不展示)。
function cacheTokensOf(
  r: { usage?: { prompt_tokens_details?: { cached_tokens?: number } } },
): number {
  return r.usage?.prompt_tokens_details?.cached_tokens ?? 0;
}

// 输入 token 中命中缓存的占比(百分比), 用于日志「缓存 N (xx%)」直观展示 prompt cache 收益;
// 无缓存或不含输入 token 时返回 null(不展示百分比)。
function cacheRateOf(
  r: { usage?: { prompt_tokens?: number; prompt_tokens_details?: { cached_tokens?: number } } },
): number | null {
  const prompt = r.usage?.prompt_tokens ?? 0;
  const cached = cacheTokensOf(r);
  if (cached <= 0 || prompt <= 0) return null;
  return (cached / prompt) * 100;
}

// 百分比展示: 四舍五入到 0.1 并去掉无意义的尾随 0 (85% / 42.3%)。
function formatPercent(rate: number): string {
  if (!Number.isFinite(rate)) return "—";
  const tenth = Math.round(rate * 10) / 10;
  return `${Number.isInteger(tenth) ? Math.round(tenth) : tenth}%`;
}

// 「缓存 N (xx%)」片段; 无缓存时返回 null。命中率 = 缓存输入 token / 总输入 token。
function CacheSuffix({
  r,
  className,
}: {
  r: { usage?: { prompt_tokens?: number; prompt_tokens_details?: { cached_tokens?: number } } };
  className?: string;
}) {
  const cached = cacheTokensOf(r);
  if (cached <= 0) return null;
  const rate = cacheRateOf(r);
  return (
    <>
      {" / 缓存 "}
      {formatNumber(cached)}
      {rate != null && <span className={className}> ({formatPercent(rate)})</span>}
    </>
  );
}

type Tab = "live" | "err";

type SSEStatus = "connecting" | "open" | "closed";

const STATE_TONE: Record<string, "neutral" | "info" | "success" | "danger"> = {
  running: "info",
  committed: "info",
  success: "success",
  failed: "danger",
  canceled: "neutral",
};

/** 后端状态 → 中文展示；未知状态回退原文。 */
const STATE_LABEL: Record<string, string> = {
  running: "正在请求",
  committed: "响应中",
  success: "成功",
  failed: "失败",
  canceled: "已取消",
};

const ERR_CLASS_OPTIONS = [
  "zero_output",
  "early_eof",
  "timeout",
  "client_cancel",
  "admin_abort",
  "upstream_4xx",
  "upstream_5xx",
  "upstream_network",
  "upstream_error",
];

export default function LogsPage() {
  const qc = useQueryClient();
  const [tab, setTab] = useState<Tab>("live");
  const [live, setLive] = useState<RequestState[]>([]);
  // 只存追踪 ID, 实际请求状态从 live 数组派生: SSE 每次更新 live 时 tracing 自动
  // 拿到最新快照, 详情弹窗的状态/时间线/响应区随之实时刷新, 不会停留在点击瞬间的旧快照。
  const [tracingId, setTracingId] = useState<number | null>(null);
  const tracing = useMemo(
    () => (tracingId !== null ? live.find((r) => r.id === tracingId) ?? null : null),
    [live, tracingId],
  );
  const [errClass, setErrClass] = useState("");
  const [confirmClearErrors, setConfirmClearErrors] = useState(false);
  const [sseStatus, setSseStatus] = useState<SSEStatus>("connecting");

  // 实时 SSE 订阅
  const sseRef = useRef<{ close: () => void } | null>(null);
  // 每个句柄一个自增 token；异步回调（onOpen/onError 可能晚于 cleanup 触发）
  // 只在 token 仍是最新时才允许写 sseStatus，避免快速切换 Tab 时指示灯抖动。
  const sseTokenRef = useRef(0);
  useEffect(() => {
    if (tab !== "live") {
      sseRef.current?.close();
      sseRef.current = null;
      setSseStatus("closed");
      return;
    }
    const token = ++sseTokenRef.current;
    setSseStatus("connecting");
    const handle = openSSE<RequestState>("/api/v1/log/overview/stream", {
      // 后端使用 event: log 发送命名 SSE 事件；不订阅 eventName 时，浏览器
      // 不会把这些事件交给 onmessage，日志页会一直显示空列表。
      eventName: "log",
      onOpen: () => {
        if (sseTokenRef.current === token) setSseStatus("open");
      },
      onError: () => {
        if (sseTokenRef.current === token) setSseStatus("closed");
      },
      onMessage: (req) => {
        setLive((prev) => {
          // 后端快照按请求 ID 倒序（最新在前）发出，列表需与之保持一致，保持
          // 「最新请求在顶部、依次向下为更早的日期」。命中已有记录时原地替换，
          // 新增记录按 ID 插入到「首个更小 ID 之前」；找不到更小 ID（即 req 为最新）
          // 时追加到末尾，避免最新请求被挤到最旧之下造成整体翻转。
          const index = prev.findIndex((r) => r.id === req.id);
          if (index >= 0) {
            const next = prev.slice();
            next[index] = req;
            return next;
          }
          const position = prev.findIndex((r) => r.id < req.id);
          if (position < 0) {
            return [...prev, req].slice(0, 200);
          }
          return (
            [...prev.slice(0, position), req, ...prev.slice(position)].slice(0, 200)
          );
        });
      },
    });
    sseRef.current = handle;
    return () => {
      handle.close();
      if (sseTokenRef.current === token) {
        // 失效本句柄 token：晚到的 onOpen/onError 不再写状态。
        sseTokenRef.current += 1;
        sseRef.current = null;
        setSseStatus("closed");
      }
    };
  }, [tab]);

  // 拉取错误日志（持久化）
  const {
    data: errors,
    isLoading: loadingErr,
    isError: errorsError,
    refetch: refetchErrors,
  } = useQuery({
    queryKey: ["log-errors", errClass],
    queryFn: () => api.listErrorLogs(50, errClass || undefined),
  });

  // 停止 / 恢复
  const { data: stopState } = useQuery({
    queryKey: ["log-stop-all-state"],
    queryFn: api.stopAllState,
    refetchInterval: 5000,
  });

  const stopStateKey = ["log-stop-all-state"] as const;
  const stopAllMut = useMutation({
    mutationFn: () => api.stopAll(),
    onMutate: () => qc.cancelQueries({ queryKey: stopStateKey }),
    onSuccess: async (result) => {
      toast.success(`已请求停止 ${result.stopped} 个运行中请求`);
      await qc.cancelQueries({ queryKey: stopStateKey });
      qc.setQueryData(stopStateKey, { is_stopped: true });
    },
    onError: (e: Error) => toast.error(e.message || "停止请求失败"),
  });
  const resumeAllMut = useMutation({
    mutationFn: () => api.resumeAll(),
    onMutate: () => qc.cancelQueries({ queryKey: stopStateKey }),
    onSuccess: async () => {
      toast.success("已恢复接收新请求");
      await qc.cancelQueries({ queryKey: stopStateKey });
      qc.setQueryData(stopStateKey, { is_stopped: false });
    },
    onError: (e: Error) => toast.error(e.message || "恢复请求失败"),
  });

  const clearErrMut = useMutation({
    mutationFn: () => api.clearErrorLogs(),
    onSuccess: () => {
      toast.success("已清空错误日志");
      setConfirmClearErrors(false);
      qc.invalidateQueries({ queryKey: ["log-errors"] });
    },
    onError: (e: Error) => toast.error(e.message || "清空错误日志失败"),
  });

  const stats = useMemo(() => {
    const running = live.filter((l) => l.status === "running" || l.status === "committed").length;
    const success = live.filter((l) => l.status === "success").length;
    const failed = live.filter((l) => l.status === "failed").length;
    return { running, success, failed, total: live.length };
  }, [live]);

  // 正在运行（含「正在请求」与「响应中」）的请求置顶展示，便于优先盯住仍在
  // 进行中的请求；同一分组内部仍保持后端 ID 倒序（最新在顶）。live 本身仍按
  // ID 倒序维护，这里只影响展示层，tracing / stats 继续基于 live。
  const isActiveRequest = (r: RequestState) =>
    r.status === "running" || r.status === "committed";
  const visibleLive = useMemo(() => {
    const active = live.filter(isActiveRequest);
    const rest = live.filter((r) => !isActiveRequest(r));
    return [...active, ...rest];
  }, [live]);

  return (
    <div className="space-y-4">
      {/* 计数条 */}
      <div className="glass-panel flex flex-wrap items-center gap-4 rounded-card px-4 py-2.5 text-xs">
        <Counter label="运行" value={stats.running} tone="info" />
        <Counter label="成功" value={stats.success} tone="success" />
        <Counter label="失败" value={stats.failed} tone="danger" />
        <Counter label="总计" value={stats.total} />
        <span
          className={cn(
            "ml-auto flex items-center gap-1.5",
            sseStatus === "open" ? "text-emerald-500" : "text-ink-muted",
            sseStatus === "closed" && "text-warning",
          )}
          aria-live="polite"
        >
          <span
            className={cn(
              "relative flex h-2 w-2",
              sseStatus !== "open" && "opacity-60",
            )}
          >
            {sseStatus === "open" && (
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-500/60" />
            )}
            <span
              className={cn(
                "relative inline-flex h-2 w-2 rounded-full",
                sseStatus === "open" && "bg-emerald-500",
                sseStatus === "connecting" && "bg-info",
                sseStatus === "closed" && "bg-warning",
              )}
            />
          </span>
          {tab !== "live"
            ? "未订阅实时流"
            : sseStatus === "open"
              ? "实时流已连接"
              : sseStatus === "connecting"
                ? "正在连接实时流…"
                : "实时流断开，自动重连中"}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1.5"
          onClick={() =>
            stopState?.is_stopped ? resumeAllMut.mutate() : stopAllMut.mutate()
          }
        >
          {stopState?.is_stopped ? (
            <>
              <Play className="h-3.5 w-3.5" aria-hidden />
              恢复接收
            </>
          ) : (
            <>
              <Pause className="h-3.5 w-3.5" aria-hidden />
              停止全部
            </>
          )}
        </Button>
      </div>

      <SegmentedControl
        aria-label="日志类型"
        size="md"
        value={tab}
        onChange={setTab}
        options={[
          { value: "live", label: "实时请求" },
          { value: "err", label: "错误日志" },
        ]}
      />

      {tab === "live" ? (
        <LiveTable rows={visibleLive} onPick={(r) => setTracingId(r.id)} />
      ) : (
        <Card>
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5 text-xs text-ink-muted">
            <div className="flex items-center gap-2">
              <span>类别</span>
              <Select
                className="h-7 text-xs"
                aria-label="错误类别"
                value={errClass}
                onChange={(e) => setErrClass(e.target.value)}
              >
                <option value="">全部</option>
                {ERR_CLASS_OPTIONS.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </Select>
              {/* errors 是「当前筛选 + limit 50」的子集，不能写成「共 N 条」误导为全量计数 */}
              <span>· 已载入 {errors?.length ?? 0} 条</span>
            </div>
            <Button
              variant="danger-outline"
              size="sm"
              className="h-7 gap-1"
              onClick={() => setConfirmClearErrors(true)}
              loading={clearErrMut.isPending}
            >
              <Trash2 className="h-3 w-3" aria-hidden />
              清空错误日志
            </Button>
          </div>
          <div className="divide-y divide-border">
            {loadingErr ? (
              <div className="flex items-center justify-center gap-2 px-4 py-8"><span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" /><span className="text-sm text-ink-muted">加载中</span></div>
            ) : errorsError ? (
              <QueryErrorBanner onRetry={() => refetchErrors()} />
            ) : (errors?.length ?? 0) === 0 ? (
              <EmptyState title="暂无错误" hint="当前筛选下没有持久化错误记录" />
            ) : (
              errors!.map((e) => <ErrorRow key={e.id} e={e} />)
            )}
          </div>
        </Card>
      )}

      {/* 追踪 Sheet */}
      <TraceSheet req={tracing} onClose={() => setTracingId(null)} />

      <Dialog
        open={confirmClearErrors}
        onOpenChange={(open) => !open && setConfirmClearErrors(false)}
      >
        <DialogContent variant="dialog" size="sm">
          <DialogHeader>
            <DialogTitle>清空错误日志</DialogTitle>
            <DialogDescription>
              将清空全部错误日志（所有类别，与当前筛选无关；后端不支持按筛选删除）。清空后不可恢复，确定继续吗？
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="ghost" size="sm">
                取消
              </Button>
            </DialogClose>
            <Button
              variant="destructive"
              size="sm"
              loading={clearErrMut.isPending}
              onClick={() => clearErrMut.mutate()}
            >
              清空并删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/**
 * 虚拟化实时表 —— 长列表性能关键（DESIGN.md §4.3 表格规范）
 *  - 行高固定 44px（与设计令牌一致）
 *  - 容器 480px 高，超出滚动
 *  - 渲染窗口 ≈ 12 行，200 行不卡
 *  - sticky 表头用 grid 实现
 */
const ROW_HEIGHT = 44;
const COLS =
  "120px 90px minmax(280px,1fr) 90px 100px 80px 140px 160px 90px";

function useElapsedTick(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}

/**
 * 耗时单元格：500ms 计时器收敛在单元格内部，只重渲染自身，而不是整张
 * 虚拟化表格 —— 之前 now 放在 LiveTable 顶层，定时器触发迫使 200 行全部重渲染。
 * 窄列（90px）放不下「首字 X · 总耗时 Y」单行，有首字时拆成两行右对齐；
 * 无首字时回退纯总耗时单行。
 */
function ElapsedCell({ r }: { r: RequestState }) {
  const active = r.status === "running" || r.status === "committed";
  const now = useElapsedTick(active);
  const parts = elapsedParts(r, now);
  if (parts.kind === "first-total" || parts.kind === "running") {
    return (
      <div className="flex flex-col items-end leading-tight">
        <span>{parts.kind === "running" ? "正在请求" : `首字 ${parts.first}`}</span>
        <span>总耗时 {parts.total}</span>
      </div>
    );
  }
  return <>{parts.kind === "total" ? parts.total : "—"}</>;
}

function LiveTable({
  rows,
  onPick,
}: {
  rows: RequestState[];
  onPick: (r: RequestState) => void;
}) {
  const parentRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 8,
  });

  return (
    <Card className="overflow-hidden p-0">
      {/* grid 语义包裹表头 + 主体，保证 role="row" 都有 role="grid" 祖先 */}
      <div role="grid" aria-rowcount={rows.length + 1} aria-label="实时请求列表">
      {/* 表头 */}
      <div
        className="grid border-b border-border bg-card/80 px-4 py-2.5 text-left text-xs text-ink-muted backdrop-blur"
        style={{ gridTemplateColumns: COLS }}
        role="row"
        aria-rowindex={1}
      >
        <div role="columnheader" className="font-medium">时间</div>
        <div role="columnheader" className="font-medium">状态</div>
        <div role="columnheader" className="font-medium">模型 → 渠道 → 目标</div>
        <div role="columnheader" className="font-medium">中继</div>
        <div role="columnheader" className="font-medium">代理</div>
        <div role="columnheader" className="font-medium">审计</div>
        <div role="columnheader" className="font-medium">客户端</div>
        <div role="columnheader" className="text-right font-medium">Tokens（入/出/缓存）</div>
        <div role="columnheader" className="text-right font-medium">耗时</div>
      </div>

      {/* 虚拟化主体 */}
      <div
        ref={parentRef}
        className="relative overflow-auto"
        style={{ height: "min(480px, 60vh)" }}
      >
        {rows.length === 0 ? (
          <EmptyState
            icon={<Activity className="h-5 w-5" aria-hidden />}
            title="暂无实时请求"
            hint="客户端首次发起后会立即出现"
          />
        ) : (
          <div
            style={{
              height: virtualizer.getTotalSize(),
              position: "relative",
              width: "100%",
            }}
          >
            {virtualizer.getVirtualItems().map((vi) => {
              const r = rows[vi.index];
              if (!r) return null;
              return (
                <div
                  key={r.id}
                  data-index={vi.index}
                  ref={virtualizer.measureElement}
                  role="row"
                  aria-rowindex={vi.index + 2}
                  tabIndex={0}
                  onClick={() => onPick(r)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onPick(r);
                    }
                  }}
                  aria-label={`查看请求 #${r.id} 追踪`}
                  className="absolute left-0 right-0 grid cursor-pointer items-center border-b border-border/60 px-4 hover:bg-surface-subtle/60 focus:bg-surface-subtle/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  style={{
                    transform: `translateY(${vi.start}px)`,
                    height: ROW_HEIGHT,
                    gridTemplateColumns: COLS,
                  }}
                >
                  <div role="gridcell" className="mono truncate text-xs text-ink-muted">
                    {new Date(r.started_at).toLocaleTimeString("zh-CN")}
                  </div>
                  <div role="gridcell">
                    <Pill tone={STATE_TONE[r.status] ?? "neutral"}>
                      {(r.status === "running" || r.status === "committed") && (
                        <Loader2 className="size-3 animate-spin" />
                      )}
                      {STATE_LABEL[r.status] ?? r.status}
                    </Pill>
                  </div>
                  <div role="gridcell" className="mono truncate text-xs text-ink-muted">
                    {r.model} → {r.target_channel} → {r.target_model}
                  </div>
                  <div role="gridcell">
                    <Pill
                      tone={r.relay_mode === "passthrough" ? "success" : "warning"}
                    >
                      {r.relay_mode === "passthrough" ? "透传" : "转换"}
                    </Pill>
                  </div>
                  <div role="gridcell" className="truncate" title={r.proxy_addr ?? ""}>
                    {r.proxy_addr ? (
                      <Pill tone="info" dot={false}>
                        代理
                      </Pill>
                    ) : (
                      <span className="text-xs text-ink-muted">直连</span>
                    )}
                  </div>
                  <div role="gridcell">
                    {r.masked && (
                      <Pill tone="info">
                        脱敏
                      </Pill>
                    )}
                  </div>
                  <div role="gridcell" className="mono truncate text-xs text-ink-muted">
                    {r.client_ip}
                  </div>
                  <div role="gridcell" className="num text-xs text-ink-muted">
                    {formatNumber(r.usage.prompt_tokens)} /{" "}
                    {formatNumber(r.usage.completion_tokens)}
                    <CacheSuffix r={r} />
                  </div>
                  <div role="gridcell" className="num text-xs text-ink-muted">
                    <ElapsedCell r={r} />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
      </div>

      {rows.length > 0 && (
        <div className="border-t border-border bg-card/40 px-4 py-1.5 text-[11px] text-ink-muted">
          共 {rows.length} 条
        </div>
      )}
    </Card>
  );
}

function ErrorRow({ e }: { e: ErrorLog }) {
  const [open, setOpen] = useState(false);
  const hasDetail = !!(e.err_detail || e.request_body || e.mask_matches?.length);
  return (
    <div className="px-4 py-2.5">
      <div
        className="flex items-center gap-2 text-xs cursor-pointer"
        onClick={() => setOpen((o) => !o)}
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onKeyDown={(ev) => {
          if (ev.key === "Enter" || ev.key === " ") {
            ev.preventDefault();
            setOpen((o) => !o);
          }
        }}
      >
        <Pill tone="danger">{e.err_class}</Pill>
        {e.model && <span className="mono text-ink-muted">{e.model}</span>}
        {e.channel_name && (
          <span className="text-ink-muted">→ {e.channel_name}</span>
        )}
        {e.api_key_name && (
          <span className="text-ink-muted">· {e.api_key_name}</span>
        )}
        <span className="ml-auto text-ink-muted">
          {new Date(e.created_at).toLocaleString("zh-CN")}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-[11px]"
          aria-expanded={open}
          onClick={(ev) => {
            ev.stopPropagation();
            setOpen((o) => !o);
          }}
        >
          {open ? "收起" : "展开"}
        </Button>
      </div>
      <p className="mt-1 text-sm text-ink">{e.err_brief}</p>
      {open && (
        <div className="mt-2 space-y-2">
          {e.request_body && (
            <div>
              <p className="mb-1 text-[11px] font-medium text-ink-muted">请求体</p>
              <FormattedBody content={e.request_body} />
            </div>
          )}
          {e.err_detail && (
            <div>
              <p className="mb-1 text-[11px] font-medium text-ink-muted">错误详情</p>
              <FormattedBody content={e.err_detail} />
            </div>
          )}
          {/* 触发规则：读取持久化的 mask_matches（文档 07 §3.3），
              复用同一展示组件；无命中时不渲染。 */}
          <MaskMatches matches={e.mask_matches} title="触发规则" />
          {!hasDetail && (
            <p className="text-[11px] text-ink-subtle">无更多详细信息</p>
          )}
        </div>
      )}
    </div>
  );
}

function Counter({
  label,
  value,
  tone = "neutral",
}: {
  label: string;
  value: number;
  tone?: "neutral" | "info" | "success" | "danger";
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          tone === "info" && "bg-info",
          tone === "success" && "bg-success",
          tone === "danger" && "bg-destructive",
          tone === "neutral" && "bg-ink-subtle",
        )}
      />
      <span className="text-ink-muted">{label}</span>
      <span className="num font-semibold text-ink">{value}</span>
    </div>
  );
}

// ---------------- 追踪 Sheet ----------------

function TraceSheet({
  req,
  onClose,
}: {
  req: RequestState | null;
  onClose: () => void;
}) {
  // 双列布局：左列切「请求体 / 分组路由 / 脱敏命中 / 详情」，右列时间线 + 响应始终可见。
  const [leftTab, setLeftTab] = useState<"body" | "route" | "mask" | "detail">("body");
  const [body, setBody] = useState<string>("");
  const [response, setResponse] = useState<string>("");
  const [bodyLoading, setBodyLoading] = useState(false);
  const [responseLoading, setResponseLoading] = useState(false);
  const attempts = req?.attempts ?? [];

  // 进行中请求的耗时每 500ms 重渲染（与 ElapsedCell 同款定时器），否则详情底部
  // 「进行中 · 1m22s」会冻结在首次渲染的值，秒数不随时间更新。
  const detailRunning = req?.status === "running" || req?.status === "committed";
  const detailNow = useElapsedTick(detailRunning);

  // 切换请求时清空上次缓存的请求体/响应体。
  useEffect(() => {
    setBody("");
    setResponse("");
    setBodyLoading(false);
    setResponseLoading(false);
  }, [req?.id]);

  const stopMut = useMutation({
    // 后端成功返回字符串 data；请求已结束/不存在走 404，而非 {stopped:false}。
    mutationFn: (id: number) => api.stopRequest(id),
    onSuccess: () => {
      toast.success("已请求中止此请求");
      onClose();
    },
    onError: (e: Error) => {
      if (e instanceof APIError && e.status === 404) {
        toast.info("请求已结束或不存在，无需中止");
      } else {
        toast.error(e.message);
      }
    },
  });

  const interruptMut = useMutation({
    mutationFn: ({ id, round }: { id: number; round: number }) =>
      api.interruptRound(id, round),
    onSuccess: (result) => {
      if (result?.interrupted === false) {
        toast.info("该轮次已结束或请求已失效，无需中止");
      } else {
        toast.success("已请求中止当前轮次");
      }
    },
    onError: (e: Error) => toast.error(e.message),
  });

  // 请求体：弹窗打开即加载（不再等切 Tab）。
  useEffect(() => {
    const myReqId = req?.id;
    if (myReqId == null) return;
    setBodyLoading(true);
    let cancelled = false;
    api
      .getRequestBody(myReqId)
      .then((r) => {
        if (!cancelled) setBody(r);
      })
      .catch(() => {
        if (!cancelled) setBody("（拉取失败或请求体已截断）");
      })
      .finally(() => {
        if (!cancelled) setBodyLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [req?.id]);

  // 响应体：终态才拉取（running / committed 展示等待指示）。
  useEffect(() => {
    const myReqId = req?.id;
    const status = req?.status;
    if (myReqId == null || status === "running" || status === "committed") return;
    setResponseLoading(true);
    let cancelled = false;
    api
      .getResponseBody(myReqId)
      .then((r) => {
        if (!cancelled) setResponse(r);
      })
      .catch(() => {
        if (!cancelled) setResponse("（拉取失败或响应体已截断）");
      })
      .finally(() => {
        if (!cancelled) setResponseLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [req?.id, req?.status]);

  if (!req) return null;

  const requestFailed = req.status === "failed" || req.status === "canceled";
  const isRunning = req.status === "running" || req.status === "committed";

  return (
    <Dialog open={!!req} onOpenChange={(o) => !o && onClose()}>
      <DialogContent variant="wide">
        {/* ---------- 富头部 ---------- */}
        <div className="border-b border-border px-5 py-3">
          <div className="flex items-center gap-2">
            <DialogTitle className="text-sm font-semibold text-ink">
              {req.model}
            </DialogTitle>
            {isRunning ? (
              <Loader2 className="size-3.5 animate-spin text-ink-muted" />
            ) : (
              <ArrowRight className="size-3.5 text-ink-muted" />
            )}
            <Pill tone="neutral" dot={false}>
              {req.target_channel || "-"}
            </Pill>
            <span className="text-sm text-ink-muted">{req.target_model}</span>
            {req.thinking_level && (
              <Pill tone="info" dot={false}>
                <Brain className="size-3" />
                {req.thinking_level}
              </Pill>
            )}
          </div>
          <DialogDescription className="mt-1.5 flex flex-wrap items-center gap-1.5">
            <Pill tone={STATE_TONE[req.status] ?? "neutral"} dot={false}>
              {isRunning && <Loader2 className="size-3 animate-spin" />}
              {STATE_LABEL[req.status] ?? req.status}
            </Pill>
            <Pill tone="neutral" dot={false} className="mono text-[10px]">
              {req.client_format} → {req.upstream_type}
            </Pill>
            <Pill
              tone={req.relay_mode === "passthrough" ? "success" : "warning"}
              dot={false}
            >
              {req.relay_mode === "passthrough" ? "协议透传" : "协议转换"}
            </Pill>
            <Pill tone={req.proxy_addr ? "info" : "neutral"} dot={false}>
              {req.proxy_addr ? `出口代理 ${req.proxy_addr}` : "直连（未走代理）"}
            </Pill>
            {req.masked && (
              <Pill tone="info" dot={false}>
                已脱敏
              </Pill>
            )}
            <span>· 客户端 {req.client_ip}</span>
            <span>
              · 密钥{" "}
              {req.key_name ?? (req.api_key ? `尾缀 ${req.api_key}` : "—")}
            </span>
            <span>· {new Date(req.started_at).toLocaleString("zh-CN")}</span>
          </DialogDescription>
        </div>

        {/* ---------- 双列网格 ---------- */}
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 p-4 md:grid-cols-2">
          {/* 左列：请求体 / 分组路由 / 脱敏命中 / 详情 */}
          <div className="flex min-h-0 flex-col overflow-hidden rounded-card border border-border bg-surface-subtle/20">
            <div className="flex h-9 shrink-0 items-center gap-1 border-b border-border px-2">
              {(["body", "route", "mask", "detail"] as const).map((t) => (
                <button
                  key={t}
                  onClick={() => setLeftTab(t)}
                  className={cn(
                    "rounded-control px-2.5 py-1 text-xs transition-colors",
                    leftTab === t
                      ? "bg-primary/10 font-medium text-primary-text"
                      : "text-ink-muted hover:text-ink",
                  )}
                >
                  {t === "body"
                    ? "请求体"
                    : t === "route"
                      ? "分组路由"
                      : t === "mask"
                        ? "脱敏命中"
                        : "详情"}
                </button>
              ))}
            </div>
            <div className="min-h-0 flex-1 overflow-auto p-3">
              {leftTab === "body" ? (
                <div className="space-y-3">
                  <FormattedBody
                    content={bodyLoading ? "" : body}
                    loading={bodyLoading}
                  />
                </div>
              ) : leftTab === "route" ? (
                <RouteTab req={req} attempts={attempts} />
              ) : leftTab === "mask" ? (
                <MaskTab req={req} />
              ) : (
                <DetailTab req={req} attempts={attempts} now={detailNow} />
              )}
            </div>
          </div>

          {/* 右列：时间线 + 响应/错误（保持原有样式与交互） */}
          <div className="flex min-h-0 flex-col gap-3">
            {/* 时间线面板 */}
            {attempts.length > 0 && (
              <div
                className="flex shrink flex-col overflow-hidden rounded-card border border-border bg-surface-subtle/20"
                style={{ maxHeight: "45%" }}
              >
                <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
                  <span className="text-sm font-medium text-ink">
                    尝试时间线
                  </span>
                  <Pill tone="neutral" dot={false} className="ml-auto">
                    {attempts.length}
                  </Pill>
                </div>
                <div className="min-h-0 flex-1 overflow-auto">
                  <ol className="divide-y divide-border">
                    {/* 倒序渲染：最新轮次在顶部，打开弹窗即可见当前尝试状态，
                        无需滚动到底部寻找进行中的条目。 */}
                    {[...attempts].reverse().map((a) => (
                      <AttemptLine key={a.seq} a={a} />
                    ))}
                  </ol>
                </div>
              </div>
            )}

            {/* 响应 / 错误面板 */}
            <div className="flex flex-1 flex-col overflow-hidden rounded-card border border-border bg-surface-subtle/20 min-h-0">
              <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
                <span className="text-sm font-medium text-ink">
                  {requestFailed ? "错误信息" : "响应内容"}
                </span>
                {isRunning && (
                  <div className="ml-auto flex items-center gap-1.5">
                    {req.status === "running" &&
                      req.sending &&
                      req.round > 0 && (
                        <button
                          type="button"
                          disabled={interruptMut.isPending}
                          onClick={() =>
                            interruptMut.mutate({
                              id: req.id,
                              round: req.round,
                            })
                          }
                          title="仅中止当前一轮次；不终止整个请求"
                          className="flex items-center gap-1.5 rounded-control px-2 py-1 text-xs text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
                        >
                          {interruptMut.isPending ? (
                            <Loader2 className="size-3.5 animate-spin" />
                          ) : (
                            <Square className="size-3.5" />
                          )}
                          中止轮次
                        </button>
                      )}
                    <button
                      type="button"
                      disabled={stopMut.isPending}
                      onClick={() => stopMut.mutate(req.id)}
                      className="flex items-center gap-1.5 rounded-control px-2 py-1 text-xs font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
                    >
                      {stopMut.isPending ? (
                        <Loader2 className="size-3.5 animate-spin" />
                      ) : (
                        <Square className="size-3.5 fill-destructive" />
                      )}
                      终止请求
                    </button>
                  </div>
                )}
              </div>
              <div className="min-h-0 flex-1 overflow-auto p-3">
                {isRunning ? (
                  <div className="flex h-full items-center justify-center gap-2 text-xs text-ink-muted">
                    <Loader2 className="size-4 animate-spin" />
                    {req.status === "committed"
                      ? "响应流式提交中…"
                      : "等待响应…"}
                  </div>
                ) : (
                  <FormattedBody
                    content={responseLoading ? "" : response}
                    loading={responseLoading}
                  />
                )}
              </div>
            </div>
          </div>
        </div>

        {/* ---------- 底部彩色指标 + 关闭 ---------- */}
        <div className="flex flex-wrap items-center gap-4 border-t border-border px-5 py-2.5 text-xs text-ink-muted">
          <div className="flex items-center gap-1.5">
            <Clock className="size-3.5 text-blue-500" />
            <span className="tabular-nums">
              {new Date(req.started_at).toLocaleTimeString("zh-CN")}
            </span>
          </div>
          <div className="flex items-center gap-1.5">
            <Cpu className="size-3.5 text-blue-500" />
            <span>{formatElapsedWithFirst(req, detailNow)}</span>
          </div>
          <div className="flex items-center gap-1.5">
            <ArrowDownToLine className="size-3.5 text-emerald-500" />
            <span className="tabular-nums">
              tokens {formatNumber(req.usage.prompt_tokens)} /{" "}
              {formatNumber(req.usage.completion_tokens)}
              <CacheSuffix r={req} />
            </span>
            {req.usage_estimated && (
              <span className="text-ink-subtle">（估算）</span>
            )}
          </div>
          <div className="ml-auto flex items-center gap-2">
            <DialogClose asChild>
              <Button variant="ghost" size="sm">
                关闭
              </Button>
            </DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function MaskTab({ req }: { req: RequestState }) {
  if (!req.mask_matches || req.mask_matches.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-ink-muted">无脱敏命中</p>
    );
  }
  return (
    <MaskMatches
      matches={req.mask_matches}
      truncated={req.mask_matches_truncated}
      caption=""
    />
  );
}

/** Token/s 展示：低速保留 1 位小数，高速不输出无意义小数。 */
function formatTps(value: number) {
  if (!Number.isFinite(value)) return "—";
  return value >= 100 ? value.toFixed(0) : value.toFixed(1);
}

/**
 * 详情 Tab：请求级诊断信息汇总。头部信息较多，详情 Tab 用稳定的
 * 键值列表把这些字段集中起来，便于审计时按需查看当前快照。
 */
function DetailTab({
  req,
  attempts,
  now,
}: {
  req: RequestState;
  attempts: AttemptRecord[];
  now: number;
}) {
  const firstTokenAt = req.first_token_at
    ? new Date(req.first_token_at).toLocaleString("zh-CN")
    : "—";

  // 最后一次失败尝试：attempts 按轮次升序，倒序取首个 outcome===failed，
  // 即「最后一次失败」的错误类别与摘要，供失败终态在详情里直出失败原因。
  const lastFailure = [...attempts].reverse().find(
    (a) => a.outcome === "failed",
  );

  // 耗时指标：完整总耗时 = 请求到达 → 完成；首字耗时 = 请求到达 → 首字；
  // 响应完成耗时 = 首字 → 完成（输出阶段）。完整总耗时与请求列表/底部指标
  // 使用同一口径：进行中按 now - started_at，终态按后端 duration_ms。
  const startedMs = new Date(req.started_at).getTime();
  const validStart = Number.isFinite(startedMs);
  const terminal = req.status !== "running" && req.status !== "committed";
  const firstAtMs = req.first_token_at
    ? new Date(req.first_token_at).getTime()
    : null;
  const firstValid =
    validStart &&
    firstAtMs != null &&
    Number.isFinite(firstAtMs) &&
    firstAtMs >= startedMs;
  const fullMs = (() => {
    if (!validStart) {
      return terminal
        ? (req.duration_ms ??
            (req.duration != null ? req.duration / 1_000_000 : null))
        : null;
    }
    if (!terminal) return Math.max(0, now - startedMs);
    return (
      req.duration_ms ??
      (req.duration != null ? req.duration / 1_000_000 : null)
    );
  })();
  const ttftMs = firstValid ? firstAtMs! - startedMs : null;
  const responseMs =
    fullMs != null && ttftMs != null ? Math.max(0, fullMs - ttftMs) : null;

  // Token/s 仅在终态有完整 tokens 与定稿耗时后才有意义；输出速度按响应完成耗时折算。
  const totalTps =
    terminal && fullMs != null && fullMs > 0
      ? req.usage.total_tokens / (fullMs / 1000)
      : null;
  const outputTps =
    terminal && responseMs != null && responseMs > 0
      ? req.usage.completion_tokens / (responseMs / 1000)
      : null;

  // 流式进行中：按累计输出字符 / 首字以来耗时实时折算输出速度 c/s(字符/秒);
  // 字符数为后端在流式转发时节流累加的 payload UTF-8 字符近似值。终态切换回 token/s。
  const liveOutputCps =
    firstValid && !terminal && firstAtMs != null && now > firstAtMs
      ? (req.output_chars ?? 0) / ((now - firstAtMs) / 1000)
      : null;

  const speedParts: string[] = [];
  if (!terminal && liveOutputCps != null && liveOutputCps > 0) {
    speedParts.push(`输出 ${formatTps(liveOutputCps)} c/s`);
  } else {
    if (totalTps != null) speedParts.push(`总 ${formatTps(totalTps)} tok/s`);
    if (outputTps != null) speedParts.push(`输出 ${formatTps(outputTps)} tok/s`);
  }
  const speedText = speedParts.length > 0 ? speedParts.join(" · ") : "—";

  return (
    <div className="space-y-3 py-2 text-xs">
      <DetailRow label="请求 ID">
        <span className="mono text-ink">{req.id}</span>
      </DetailRow>
      <DetailRow label="状态">
        <Pill tone={STATE_TONE[req.status] ?? "neutral"} dot={false}>
          {STATE_LABEL[req.status] ?? req.status}
        </Pill>
      </DetailRow>
      {req.status === "failed" && lastFailure && (
        <div
          data-testid="failed-detail"
          className="rounded-card border border-destructive/30 bg-destructive/5 p-3"
        >
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <span className="text-[11px] font-medium text-ink-muted">
              失败详情
            </span>
            <span className="text-[11px] text-ink-subtle">
              （第 {lastFailure.seq} 次尝试）
            </span>
            {lastFailure.err_class && (
              <Pill tone="danger" dot={false} className="text-[10px]">
                {lastFailure.err_class}
              </Pill>
            )}
          </div>
          <p className="whitespace-pre-wrap text-xs leading-relaxed text-destructive/90">
            {lastFailure.err_brief || "（无错误摘要）"}
          </p>
        </div>
      )}
      <DetailRow label="模型">
        <span className="text-ink">{req.model}</span>
        {req.thinking_level && (
          <span className="ml-2 inline-flex items-center gap-1 text-ink-muted">
            <Brain className="size-3" />
            {req.thinking_level}
          </span>
        )}
      </DetailRow>
      <DetailRow label="目标">
        <span className="mono text-ink">
          {req.target_channel} → {req.target_model}
        </span>
      </DetailRow>
      <DetailRow label="协议链路">
        <span className="mono text-ink">
          {req.client_format} → {req.upstream_type}
        </span>
      </DetailRow>
      <DetailRow label="中继方式">
        <span className="text-ink">
          {req.relay_mode === "passthrough" ? "协议透传" : "协议转换"}
        </span>
      </DetailRow>
      <DetailRow label="出口代理">
        <span className="mono text-ink" title={req.proxy_addr}>
          {req.proxy_addr || "直连（未走代理）"}
        </span>
      </DetailRow>
      <DetailRow label="客户端 IP">
        <span className="mono text-ink">{req.client_ip}</span>
      </DetailRow>
      <DetailRow label="密钥">
        <span className="mono text-ink">
          {req.key_name ?? (req.api_key ? `尾缀 ${req.api_key}` : "—")}
        </span>
      </DetailRow>
      <DetailRow label="脱敏">
        <Pill tone={req.masked ? "info" : "neutral"} dot={false}>
          {req.masked ? "已脱敏" : "未脱敏"}
        </Pill>
      </DetailRow>
      <DetailRow label="尝试轮次">
        <span className="num text-ink">{attempts.length}</span>
        {req.round > 0 && (
          <span className="ml-2 text-ink-muted">（当前轮次 #{req.round}）</span>
        )}
      </DetailRow>
      <DetailRow label="开始时间">
        <span className="text-ink">
          {new Date(req.started_at).toLocaleString("zh-CN")}
        </span>
      </DetailRow>
      <DetailRow label="首字时点">
        <span className="text-ink">{firstTokenAt}</span>
      </DetailRow>
      <DetailRow label="完整总耗时">
        <span className="num text-ink">
          {fullMs != null ? formatElapsed(fullMs) : "—"}
        </span>
      </DetailRow>
      <DetailRow label="首字耗时">
        <span className="num text-ink">
          {ttftMs != null
            ? formatElapsed(ttftMs)
            : terminal
              ? "未产生首字"
              : "等待首字…"}
        </span>
      </DetailRow>
      <DetailRow label="响应完成耗时">
        <span className="num text-ink">
          {terminal
            ? responseMs != null
              ? formatElapsed(responseMs)
              : "—"
            : "—"}
        </span>
      </DetailRow>
      <DetailRow label="Token 速度">
        <span className="num text-ink">{speedText}</span>
      </DetailRow>
      <DetailRow label="Tokens">
        <span className="num text-ink">
          输入 {formatNumber(req.usage.prompt_tokens)} / 输出{" "}
          {formatNumber(req.usage.completion_tokens)} / 合计{" "}
          {formatNumber(req.usage.total_tokens)}
          {cacheTokensOf(req) > 0 && (
            <span className="ml-2 text-ink-muted">
              <CacheSuffix r={req} />
            </span>
          )}
          {req.usage_estimated && (
            <span className="ml-2 text-ink-subtle">（估算）</span>
          )}
        </span>
      </DetailRow>
    </div>
  );
}

function DetailRow({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-start gap-3">
      <span className="w-20 shrink-0 text-ink-muted">{label}</span>
      <div className="min-w-0 flex-1 text-ink">{children}</div>
    </div>
  );
}

function AttemptLine({ a }: { a: AttemptRecord }) {
  const inProgress = !a.outcome;
  const outcomeTone =
    a.outcome === "success"
      ? "success"
      : a.outcome === "failed"
        ? "danger"
        : inProgress
          ? "info"
          : "neutral";
  const outcomeLabel =
    a.outcome === "success"
      ? "成功"
      : a.outcome === "failed"
        ? "失败"
        : inProgress
          ? "进行中"
          : "已取消";

  // 成功尝试：三行布局（渠道+模型+密钥 / 代理 / 首字+耗时）。
  if (a.outcome === "success") {
    return (
      <li className="flex flex-col gap-1 px-3 py-2.5 text-xs">
        {/* 第一行：渠道 + 模型名字 + 密钥 */}
        <div className="flex items-center gap-2 min-w-0">
          <span className="shrink-0 tabular-nums text-ink-muted">
            #{a.seq}
          </span>
          <span className="font-semibold text-ink truncate">
            {a.channel_name}
          </span>
          {a.key_label && (
            <Pill tone="neutral" dot={false} className="text-[10px]">
              {a.key_label}
            </Pill>
          )}
          <span className="mono truncate text-ink-muted" title={a.model}>
            {a.model}
          </span>
          <span className="ml-auto shrink-0">
            <Pill tone={outcomeTone} dot={false}>
              {outcomeLabel}
            </Pill>
          </span>
        </div>
        {/* 第二行：是否使用代理 + 代理详情 */}
        <div className="flex items-center gap-1.5 min-w-0 pl-5">
          {a.proxy_addr ? (
            <span
              className="mono truncate text-[10px] text-blue-600 dark:text-blue-400"
              title={a.proxy_addr}
            >
              代理 {a.proxy_addr}
            </span>
          ) : (
            <span className="shrink-0 text-[10px] text-ink-subtle">直连（未走代理）</span>
          )}
        </div>
        {/* 第三行：首字 + 耗时 */}
        <div className="flex items-center gap-1.5 pl-5 tabular-nums text-ink-muted">
          <Clock className="size-3" />
          {a.first_token_ms && a.first_token_ms > 0 ? (
            <span>首字 {a.first_token_ms}ms · 总耗时 {a.latency_ms}ms</span>
          ) : (
            <span>总耗时 {a.latency_ms}ms</span>
          )}
        </div>
      </li>
    );
  }

  // 非成功尝试：保持原有单行布局。
  return (
    <li className="flex flex-col gap-1.5 px-3 py-2.5 text-xs">
      <div className="flex items-center gap-2 min-w-0">
        <span className="shrink-0 tabular-nums text-ink-muted">
          #{a.seq}
        </span>
        <span className="font-semibold text-ink truncate">
          {a.channel_name}
        </span>
        {a.key_label && (
          <Pill tone="neutral" dot={false} className="text-[10px]">
            {a.key_label}
          </Pill>
        )}
        <span className="mono truncate text-ink-muted" title={a.model}>
          {a.model}
        </span>
        {a.proxy_addr ? (
          <span
            className="mono truncate text-[10px] text-blue-600 dark:text-blue-400"
            title={a.proxy_addr}
          >
            代理 {a.proxy_addr}
          </span>
        ) : (
          <span className="shrink-0 text-[10px] text-ink-subtle">直连</span>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-1.5 tabular-nums text-ink-muted">
          {a.latency_ms > 0 && (
            <>
              <Clock className="size-3" />
              <span>{a.latency_ms}ms</span>
            </>
          )}
          {a.latency_ms === 0 && <span>—</span>}
          <Pill tone={outcomeTone} dot={false}>
            {inProgress && <Loader2 className="size-3 animate-spin" />}
            {outcomeLabel}
          </Pill>
        </span>
      </div>
      {a.err_brief && (
        <div className="flex items-start gap-1.5 min-w-0">
          {a.err_class && (
            <Pill tone="danger" dot={false} className="text-[10px]">
              {a.err_class}
            </Pill>
          )}
          <p
            className="min-w-0 flex-1 text-[11px] leading-relaxed text-destructive/90 line-clamp-2 whitespace-pre-wrap"
            title={a.err_brief}
          >
            {a.err_brief}
          </p>
        </div>
      )}
    </li>
  );
}

// ---------------- 分组路由全貌（REQ-017） ----------------

/**
 * 分组路由 Tab：展示请求所属分组的完整成员列表（按故障转移顺序），
 * 复用 useGroupRuntime 获取实时路由状态，并与时间线 attempts 联动标注已尝试成员。
 *
 * 单独成组件是为了让 useQuery（groups/channels）与 useGroupRuntime 的 SSE 订阅
 * 仅在用户切到「分组路由」Tab 时建立，避免在日志页常驻一个路由运行时流。
 */
function RouteTab({
  req,
  attempts,
}: {
  req: RequestState;
  attempts: AttemptRecord[];
}) {
  // req.model 即客户端模型名，亦为分组名
  const { data: groups } = useQuery({ queryKey: ["groups"], queryFn: api.listGroups });
  const { data: channels } = useQuery({
    queryKey: ["channels"],
    queryFn: api.listChannels,
  });
  const runtime = useGroupRuntime();

  const channelById = useMemo(
    () => new Map((channels ?? []).map((c) => [c.id, c])),
    [channels],
  );

  const group = useMemo(
    () => (groups ?? []).find((g) => g.name === req.model),
    [groups, req.model],
  );

  // 按优先级升序排列（故障转移顺序）
  const items = useMemo(
    () => (group?.items ?? []).slice().sort((a, b) => a.priority - b.priority),
    [group],
  );

  if (!group) {
    return (
      <div className="space-y-3 py-4">
        <p className="text-sm text-ink-muted">该请求未经过分组路由，直接使用以下渠道：</p>
        <div className="rounded-lg border border-border bg-card/40 p-3 space-y-2 text-sm">
          <div className="flex items-center gap-2">
            <span className="text-ink-muted min-w-[80px]">渠道</span>
            <span className="mono text-ink">{req.target_channel}</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-ink-muted min-w-[80px]">模型</span>
            <span className="mono text-ink">{req.target_model}</span>
          </div>
          {(req.key_name || req.api_key) && (
            <div className="flex items-center gap-2">
              <span className="text-ink-muted min-w-[80px]">密钥</span>
              <span className="mono text-ink">
                {req.key_name || "—"}{req.api_key ? ` (${req.api_key})` : ""}
              </span>
            </div>
          )}
          <div className="flex items-center gap-2">
            <span className="text-ink-muted min-w-[80px]">中继方式</span>
            <Pill tone="neutral">{req.relay_mode === "passthrough" ? "透传" : "转换"}</Pill>
          </div>
          {req.masked && (
            <div className="flex items-center gap-2">
              <span className="text-ink-muted min-w-[80px]">审计</span>
              <Pill tone="info">已脱敏</Pill>
            </div>
          )}
        </div>
      </div>
    );
  }
  if (items.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-ink-muted">该分组暂无成员</p>
    );
  }

  const routeState = runtime.get(group.id);

  return (
    <ol className="space-y-2">
      {items.map((item, idx) => {
        // 引用成员（ref_group_name 非空）不直接绑定渠道，渠道名/启用态均不适用
        const channel = item.ref_group_name
          ? undefined
          : channelById.get(item.channel_model?.channel_id ?? 0);
        // 用 member_id 精确匹配时间线中的尝试记录（比 channel_name 更可靠）
        const attempt = attempts.find((a) => a.member_id === item.id);
        return (
          <RouteMemberRow
            key={item.id}
            index={idx + 1}
            item={item}
            channelName={channel?.name}
            channelEnabled={channel?.enabled}
            routeState={routeState}
            attempt={attempt}
          />
        );
      })}
    </ol>
  );
}

function RouteMemberRow({
  index,
  item,
  channelName,
  channelEnabled,
  routeState,
  attempt,
}: {
  index: number;
  item: GroupItem;
  channelName?: string;
  channelEnabled?: boolean;
  routeState?: GroupRouteState;
  attempt?: AttemptRecord;
}) {
  // 引用成员展示「→ 引用分组名」；普通成员展示「渠道名 → 模型名」
  const label = item.ref_group_name
    ? `→ ${item.ref_group_name}`
    : `${channelName ?? "?"} → ${item.channel_model?.name ?? `#${item.channel_model_id}`}`;

  return (
    <li className="flex items-center gap-3 rounded-md border border-border bg-card/60 px-3 py-2">
      <span className="num w-6 shrink-0 text-center text-xs text-ink-muted">
        {index}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="mono truncate text-ink">{label}</span>
          <Pill tone="neutral" className="text-[10px]">
            #{item.priority}
          </Pill>
        </div>
        {/* 与时间线联动：已尝试过的成员标注轮次与结果 */}
        {attempt && (
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-ink-muted">
            <span>轮次 #{attempt.seq}</span>
            <Pill
              tone={
                attempt.outcome === "success"
                  ? "success"
                  : attempt.outcome === "failed"
                    ? "danger"
                    : attempt.outcome
                      ? "neutral"
                      : "info"
              }
              className="text-[10px]"
            >
              {!attempt.outcome && <Loader2 className="size-3 animate-spin" />}
              {attempt.outcome === "success"
                ? "成功"
                : attempt.outcome === "failed"
                  ? "失败"
                  : attempt.outcome
                    ? "已取消"
                    : "进行中"}
            </Pill>
            {attempt.err_class && (
              <span className="text-ink-muted">· {attempt.err_class}</span>
            )}
            {attempt.latency_ms > 0 && (
              <span className="num text-ink-muted">· {attempt.latency_ms}ms</span>
            )}
            {attempt.proxy_addr && (
              <span className="mono truncate text-ink-muted" title={attempt.proxy_addr}>
                · 代理 {attempt.proxy_addr}
              </span>
            )}
          </div>
        )}
      </div>
      <RouteMemberStatus
        state={routeState}
        itemId={item.id}
        channelEnabled={channelEnabled}
      />
    </li>
  );
}

/**
 * 成员实时状态标签：渠道已禁用 / 冷却中(含倒计时) / 半开探测中 / 当前承载 / 亲和中 / 可用。
 * useNow 只让本组件每秒重渲染（倒计时），不拖动整页（与 Groups.tsx MemberRuntimeChips 同款约束）。
 */
function RouteMemberStatus({
  state,
  itemId,
  channelEnabled,
}: {
  state?: GroupRouteState;
  itemId: number;
  channelEnabled?: boolean;
}) {
  const now = useNow();

  // 渠道已禁用（仅普通成员；引用成员 channelEnabled 为 undefined，跳过）
  if (channelEnabled === false) {
    return <Pill tone="neutral">渠道已禁用</Pill>;
  }
  if (!state) {
    return <Pill tone="neutral">可用</Pill>;
  }

  // 冷却中（含倒计时）
  const cooldownLeft = remainingSeconds(
    state.cooldowns?.[String(itemId)] ?? 0,
    now,
  );
  if (cooldownLeft > 0) {
    return (
      <Pill tone="danger">冷却中 {formatCountdown(cooldownLeft)}</Pill>
    );
  }

  // 半开探测中（probe_item_id 或 half_opens 命中）
  if (state.probe_item_id === itemId || state.half_opens?.[String(itemId)]) {
    return <Pill tone="warning">半开探测中</Pill>;
  }

  // 当前承载；若同时处于亲和窗口则展示「亲和中」含倒计时
  if (state.current_item_id === itemId) {
    const affinityLeft = remainingSeconds(state.affinity_until, now);
    if (affinityLeft > 0) {
      return (
        <Pill tone="info">亲和中 {formatCountdown(affinityLeft)}</Pill>
      );
    }
    return <Pill tone="success">当前承载</Pill>;
  }

  return <Pill tone="neutral">可用</Pill>;
}
