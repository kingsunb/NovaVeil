import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ListChecks, Plus, RefreshCw, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { AUTO_GROUP_NAME, extractRenderableHtml, formatEvalTime, type EvalRankSummary } from "@/lib/model-eval";
import { formatNumber } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { Skeleton } from "@/components/ui/skeleton";
import { EvalOutcomeBadge } from "./EvalOutcomeBadge";
import { SandboxPreview } from "./SandboxPreview";

export function EvalRanking({ busy, onShowHistory }: { busy: boolean; onShowHistory: (channelId: number, modelName?: string) => void }) {
  const qc = useQueryClient();
  const [expanded, setExpanded] = useState<Record<number, boolean>>({});
  const ranksQuery = useQuery({
    queryKey: ["model-eval", "rank", "list"],
    queryFn: ({ signal }) => api.listEvalRanks(signal),
    staleTime: 0,
  });
  const groupsQuery = useQuery({ queryKey: ["groups"], queryFn: api.listGroups });
  const rankable = (ranksQuery.data?.items ?? []).filter((r) => r.outcome === "ok" || r.outcome === "violation");
  const existingAuto = groupsQuery.data?.find((g) => g.name === AUTO_GROUP_NAME);

  const applyAutoMut = useMutation({
    mutationFn: () => api.applyProGroup(),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["groups"] });
      toast.success(`分组 ${AUTO_GROUP_NAME} 已${res.created ? "创建" : "更新"}，共 ${res.item_count} 个成员`);
    },
    onError: (e: Error) => toast.error(e.message || "更新分组失败"),
  });

  const moveMut = useMutation({
    mutationFn: (vars: { id: number; direction: -1 | 1 } | { id: number; position: number }) =>
      "position" in vars ? api.setEvalRankPosition(vars.id, vars.position) : api.moveEvalRank(vars.id, vars.direction),
    onMutate: () => qc.cancelQueries({ queryKey: ["model-eval", "rank", "list"] }),
    onSuccess: (data) => qc.setQueryData(["model-eval", "rank", "list"], data),
    onError: (e: Error) => toast.error(e.message || "调整排序失败"),
    onSettled: () => qc.invalidateQueries({ queryKey: ["model-eval", "rank", "list"] }),
  });
  const removeMut = useMutation({
    mutationFn: (id: number) => api.removeEvalRank(id),
    onMutate: () => qc.cancelQueries({ queryKey: ["model-eval", "rank", "list"] }),
    onSuccess: (data) => qc.setQueryData(["model-eval", "rank", "list"], data),
    onError: (e: Error) => toast.error(e.message || "移除失败"),
    onSettled: () => qc.invalidateQueries({ queryKey: ["model-eval", "rank", "list"] }),
  });
  const rankBusy = busy || moveMut.isPending || removeMut.isPending || applyAutoMut.isPending;

  function toggle(id: number) {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }));
  }

  return (
    <Card className="min-w-0 overflow-hidden">
      <div className="space-y-2 border-b border-border/50 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <ListChecks className="h-4 w-4 text-ink-muted" aria-hidden />
            <h2 className="text-sm font-semibold text-ink">评估排序 <span className="font-normal text-ink-subtle">· {rankable.length} 个成功模型</span></h2>
          </div>
          <div className="flex items-center gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => void ranksQuery.refetch()} disabled={rankBusy || ranksQuery.isFetching}>
              <RefreshCw className={`h-3.5 w-3.5 ${ranksQuery.isFetching ? "animate-spin" : ""}`} aria-hidden />刷新
            </Button>
            <Button type="button" variant="secondary" size="sm" onClick={() => applyAutoMut.mutate()} disabled={rankBusy || rankable.length === 0 || ranksQuery.isFetching || ranksQuery.isError} loading={applyAutoMut.isPending}>
              <Plus className="h-3.5 w-3.5" aria-hidden />{existingAuto ? "更新" : "创建"} {AUTO_GROUP_NAME} 分组
            </Button>
          </div>
        </div>
        <p className="text-[11px] leading-relaxed text-ink-subtle">显示请求成功的结果，格式不符的也在内，越靠前优先级越高。失败结果不在此列表。输入名次后按回车或移开焦点保存，其他模型自动顺延；超过最大名次时排到最后。{existingAuto ? `更新将用当前排序替换 ${AUTO_GROUP_NAME} 的现有成员。` : "从历史加入时，目前只能选择格式合规的记录。"}</p>
      </div>

      {ranksQuery.isError ? (
        <div className="p-4"><QueryErrorBanner onRetry={() => void ranksQuery.refetch()} /></div>
      ) : ranksQuery.isLoading ? (
        <div className="space-y-3 p-4"><Skeleton className="h-20 w-full" /><Skeleton className="h-20 w-full" /><Skeleton className="h-20 w-full" /></div>
      ) : rankable.length === 0 ? (
        <EmptyState icon={<ListChecks className="h-5 w-5" />} title="还没有成功的排序结果" hint="请求成功的评估会自动加入排序，格式不符的也包括在内。从历史手动加入时，目前只能选择格式合规的记录。" />
      ) : (
        <ul className="divide-y divide-border/40">
          {rankable.map((record, index) => (
            <RankItem
              key={record.id}
              record={record}
              index={index}
              total={rankable.length}
              busy={rankBusy}
              expanded={!!expanded[record.id]}
              onToggle={() => toggle(record.id)}
              onMove={(direction) => moveMut.mutate({ id: record.id, direction })}
              onSetPosition={(position) => moveMut.mutate({ id: record.id, position })}
              onRemove={() => removeMut.mutate(record.id)}
              onHistory={() => onShowHistory(record.channel_id, record.model_name)}
            />
          ))}
        </ul>
      )}
    </Card>
  );
}

function RankItem({ record, index, total, busy, expanded, onToggle, onMove, onSetPosition, onRemove, onHistory }: {
  record: EvalRankSummary;
  index: number;
  total: number;
  busy: boolean;
  expanded: boolean;
  onToggle: () => void;
  onMove: (direction: -1 | 1) => void;
  onSetPosition: (position: number) => void;
  onRemove: () => void;
  onHistory: () => void;
}) {
  const [draft, setDraft] = useState<string | null>(null);
  const [previewVisible, setPreviewVisible] = useState(false);
  const rowRef = useRef<HTMLLIElement>(null);
  useEffect(() => {
    const row = rowRef.current;
    if (!row) return;
    if (!("IntersectionObserver" in window)) {
      setPreviewVisible(true);
      return;
    }
    const observer = new IntersectionObserver(([entry]) => {
      if (entry) setPreviewVisible(entry.isIntersecting);
    }, { rootMargin: "160px 0px" });
    observer.observe(row);
    return () => observer.disconnect();
  }, []);

  const contentQuery = useQuery({
    // 同一个排序条目重测后会更换来源记录，预览缓存随之更新。
    queryKey: ["model-eval", "rank", "content", record.id, record.source_eval_id],
    queryFn: ({ signal }) => api.getEvalRankContent(record.id, signal),
    enabled: previewVisible || expanded,
    staleTime: Infinity,
  });
  const html = useMemo(() => extractRenderableHtml(contentQuery.data?.content ?? ""), [contentQuery.data?.content]);

  function commitPosition(raw: string) {
    setDraft(null);
    const value = raw.trim();
    if (busy || value === "") return;
    if (!/^\d+$/.test(value) || Number(value) < 1) {
      toast.error("请输入大于 0 的整数名次");
      return;
    }
    // 由服务端按最新模型总数处理越界，避免队列新增结果时误排到倒数第二。
    const position = Math.min(Number(value), Number.MAX_SAFE_INTEGER);
    if (position !== index + 1) onSetPosition(position);
  }

  return (
    <li ref={rowRef} className="min-w-0 space-y-3 p-4">
      <div className="grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)_auto] items-start gap-x-3 gap-y-2 sm:grid-cols-[3.25rem_7.5rem_minmax(0,1fr)_auto]">
        <div className="row-span-2 flex flex-col items-center gap-0.5 sm:row-span-1">
          <Input
            type="number"
            inputMode="numeric"
            min={1}
            step={1}
            value={draft ?? String(index + 1)}
            disabled={busy}
            onChange={(event) => setDraft(event.target.value)}
            onFocus={(event) => event.currentTarget.select()}
            onBlur={(event) => commitPosition(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                event.currentTarget.blur();
              } else if (event.key === "Escape") {
                event.preventDefault();
                setDraft(null);
                event.currentTarget.value = String(index + 1);
                event.currentTarget.blur();
              }
            }}
            className="no-spin h-7 px-1 text-center text-xs font-semibold tabular-nums"
            aria-label={`${record.channel_name} ${record.model_name} 的排序名次`}
            title="输入名次后按回车或移开焦点保存，超过最大名次时排到最后"
          />
          <Button type="button" variant="ghost" size="icon" className="h-6 w-6" onClick={() => onMove(-1)} disabled={busy || index === 0} aria-label={`上移 ${record.model_name}`}><ArrowUp className="h-3.5 w-3.5" aria-hidden /></Button>
          <Button type="button" variant="ghost" size="icon" className="h-6 w-6" onClick={() => onMove(1)} disabled={busy || index === total - 1} aria-label={`下移 ${record.model_name}`}><ArrowDown className="h-3.5 w-3.5" aria-hidden /></Button>
        </div>
        <button
          type="button"
          onClick={onToggle}
          className="col-start-2 row-start-2 h-20 w-[7.5rem] rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 sm:row-start-1"
          aria-label={`${expanded ? "收起" : "展开"} ${record.channel_name} ${record.model_name} 的评估预览`}
          aria-expanded={expanded}
          aria-controls={`eval-rank-preview-${record.id}`}
          title="点击查看大预览"
        >
          {!previewVisible || contentQuery.isLoading ? (
            <Skeleton className="h-full w-full rounded-lg" />
          ) : contentQuery.isError ? (
            <span className="flex h-full items-center justify-center rounded-lg border border-border/50 bg-ink/[0.02] text-[11px] text-ink-muted">预览加载失败</span>
          ) : html ? (
            <SandboxPreview html={html} title={`${record.channel_name} · ${record.model_name} 缩略预览`} thumbnail />
          ) : (
            <span className="flex h-full items-center justify-center rounded-lg border border-border/50 bg-ink/[0.02] text-[11px] text-ink-subtle">暂无预览</span>
          )}
        </button>
        <div className="col-start-2 row-start-1 min-w-0 sm:col-start-3">
          <p className="break-all font-mono text-[13px] font-medium text-ink">{record.model_name}</p>
          <p className="mt-1 break-all text-xs text-ink-muted">{record.channel_name}</p>
          <time dateTime={record.created_at} className="mt-1 block text-[11px] tabular-nums text-ink-subtle">{formatEvalTime(record.created_at)}</time>
        </div>
        <Button type="button" variant="ghost" size="icon" className="col-start-3 row-start-1 h-7 w-7 sm:col-start-4" disabled={busy} onClick={onRemove} aria-label={`移除 ${record.model_name}`} title="从排序移除，保留历史"><X className="h-3.5 w-3.5" aria-hidden /></Button>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 pl-16">
        <EvalOutcomeBadge outcome={record.outcome} />
        <span className="text-[11px] tabular-nums text-ink-subtle">{formatNumber(record.latency_ms)} ms · {formatNumber(record.completion_tokens)} 输出 tok</span>
        <div className="flex flex-wrap items-center gap-1 sm:ml-auto">
          <Button type="button" variant="ghost" size="sm" onClick={onToggle} aria-expanded={expanded} aria-controls={`eval-rank-preview-${record.id}`}>{expanded ? "收起预览" : "查看预览"}</Button>
          <Button type="button" variant="ghost" size="sm" onClick={onHistory}>历史</Button>
        </div>
      </div>
      {record.error && <p className="line-clamp-3 break-all pl-16 text-xs leading-relaxed text-destructive">{record.error}</p>}
      {expanded && (
        <RankPreview
          id={record.id}
          channelName={record.channel_name}
          modelName={record.model_name}
          truncated={contentQuery.data?.content_truncated ?? record.content_truncated}
          html={html}
          isLoading={contentQuery.isLoading}
          isError={contentQuery.isError}
          onRetry={() => void contentQuery.refetch()}
        />
      )}
    </li>
  );
}

function RankPreview({ id, channelName, modelName, truncated, html, isLoading, isError, onRetry }: {
  id: number;
  channelName: string;
  modelName: string;
  truncated: boolean;
  html: string;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
}) {
  return (
    <div id={`eval-rank-preview-${id}`} className="pl-16">
      {truncated && <p className="mb-2 rounded-lg bg-amber-500/10 p-2 text-[11px] text-amber-700 dark:text-amber-400">回复超过 1 MB，已保留前 1 MB，预览可能不完整。</p>}
      {isError ? (
        <QueryErrorBanner onRetry={onRetry} />
      ) : isLoading ? (
        <Skeleton className="h-72 w-full" />
      ) : html ? (
        <SandboxPreview html={html} title={`${channelName} · ${modelName} 评估预览`} />
      ) : (
        <EmptyState title="没有可预览的 HTML" hint="可以到历史记录查看原始回复。" className="py-6" />
      )}
    </div>
  );
}
