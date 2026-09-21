import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, ListOrdered, Loader2, Square, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { formatEvalTime, type EvalQueueTask, type QueueStatus } from "@/lib/model-eval";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { Skeleton } from "@/components/ui/skeleton";

const statusLabel: Record<QueueStatus, string> = {
  queued: "待执行",
  running: "执行中",
  done: "已完成",
  stopped: "已停止",
};

export function EvalQueue({ busy }: { busy: boolean }) {
  const qc = useQueryClient();
  const [confirmClear, setConfirmClear] = useState(false);
  const sseRef = useRef<{ close: () => void } | null>(null);
  const queueQuery = useQuery({
    queryKey: ["model-eval", "queue", "list"],
    queryFn: ({ signal }) => api.listEvalQueue(signal),
    refetchInterval: 5000,
  });

  useEffect(() => {
    const handle = api.streamEvalQueue((items) => {
      qc.setQueryData(["model-eval", "queue", "list"], { items });
    });
    sseRef.current = handle;
    return () => handle.close();
  }, [qc]);

  const queueKey = ["model-eval", "queue", "list"] as const;
  const moveUpMut = useMutation({
    mutationFn: (id: number) => api.moveUpEvalQueue(id),
    onMutate: () => qc.cancelQueries({ queryKey: queueKey }),
    onSuccess: async (data) => {
      await qc.cancelQueries({ queryKey: queueKey });
      qc.setQueryData(queueKey, data);
    },
    onError: (e: Error) => toast.error(e.message || "调整失败"),
  });
  const stopMut = useMutation({
    mutationFn: (id: number) => api.stopEvalQueue(id),
    onMutate: () => qc.cancelQueries({ queryKey: queueKey }),
    onSuccess: async (data) => {
      await qc.cancelQueries({ queryKey: queueKey });
      qc.setQueryData(queueKey, data);
    },
    onError: (e: Error) => toast.error(e.message || "停止失败"),
  });
  const clearMut = useMutation({
    mutationFn: () => api.clearEvalQueue(),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["model-eval", "queue", "list"] });
      toast.success(`已清空 ${res.removed} 个待执行任务`);
      setConfirmClear(false);
    },
    onError: (e: Error) => toast.error(e.message || "清空失败"),
  });

  // 后端仅返回 queued/running，完成或失败的任务即从队列消失，结果落库到评估历史。
  const items = queueQuery.data?.items ?? [];
  const queued = items.filter((t) => t.status === "queued");
  const running = items.filter((t) => t.status === "running");

  return (
    <Card className="min-w-0 overflow-hidden">
      <div className="space-y-2 border-b border-border/50 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <ListOrdered className="h-4 w-4 text-ink-muted" aria-hidden />
            <h2 className="text-sm font-semibold text-ink">评估队列 <span className="font-normal text-ink-subtle">· {queued.length} 待执行 / {running.length} 执行中</span></h2>
          </div>
          <Button type="button" variant="ghost" size="sm" onClick={() => setConfirmClear(true)} disabled={busy || clearMut.isPending || queued.length === 0}>
            <Trash2 className="h-3.5 w-3.5" aria-hidden />清空队列
          </Button>
        </div>
        <p className="text-[11px] leading-relaxed text-ink-subtle">新增评估排在队尾按序执行；可向上调整待执行任务顺序、停止单个任务，清空只移除待执行任务不中断执行中。</p>
      </div>

      {confirmClear && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-amber-500/20 bg-amber-500/5 px-4 py-3 text-xs">
          <span className="text-amber-700 dark:text-amber-400">确认清空全部 {queued.length} 个待执行任务？该操作不可恢复。</span>
          <div className="flex items-center gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => setConfirmClear(false)}>取消</Button>
            <Button type="button" variant="destructive" size="sm" onClick={() => clearMut.mutate()} loading={clearMut.isPending}>确认清空</Button>
          </div>
        </div>
      )}

      {queueQuery.isError ? (
        <div className="p-4"><QueryErrorBanner onRetry={() => void queueQuery.refetch()} /></div>
      ) : queueQuery.isLoading ? (
        <div className="space-y-3 p-4"><Skeleton className="h-16 w-full" /><Skeleton className="h-16 w-full" /></div>
      ) : items.length === 0 ? (
        <EmptyState icon={<ListOrdered className="h-5 w-5" />} title="队列为空" hint="在当前评估视图选择模型开始评估，任务会自动加入队列。" />
      ) : (
        <ul className="divide-y divide-border/40">
          {items.map((task) => (
            <QueueItem key={task.id} task={task} busy={busy} onMoveUp={() => moveUpMut.mutate(task.id)} onStop={() => stopMut.mutate(task.id)} movePending={moveUpMut.isPending} stopPending={stopMut.isPending} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function QueueItem({ task, busy, onMoveUp, onStop, movePending, stopPending }: {
  task: EvalQueueTask;
  busy: boolean;
  onMoveUp: () => void;
  onStop: () => void;
  movePending: boolean;
  stopPending: boolean;
}) {
  const isRunning = task.status === "running";
  const isQueued = task.status === "queued";
  return (
    <li className="min-w-0 space-y-2 p-4">
      <div className="flex items-start gap-3">
        <div className="flex w-7 shrink-0 flex-col items-center gap-0.5">
          {isRunning ? <Loader2 className="h-4 w-4 animate-spin text-primary-text" aria-hidden /> : <span className="text-xs font-semibold tabular-nums text-ink-subtle">{task.position + 1}</span>}
          {isQueued && <Button type="button" variant="ghost" size="icon" className="h-6 w-6" onClick={onMoveUp} disabled={busy || movePending} aria-label="上移" title="向上调整"><ArrowUp className="h-3.5 w-3.5" aria-hidden /></Button>}
        </div>
        <div className="min-w-0 flex-1">
          <p className="break-all font-mono text-[13px] font-medium text-ink">{task.model_name}</p>
          <p className="mt-1 break-all text-xs text-ink-muted">{task.channel_name}</p>
          <time dateTime={task.created_at} className="mt-1 block text-[11px] tabular-nums text-ink-subtle">{formatEvalTime(task.created_at)}</time>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <StatusBadge status={task.status} />
          {isQueued && <Button type="button" variant="ghost" size="icon" className="h-7 w-7" onClick={onStop} disabled={busy || stopPending} aria-label="停止" title="停止该任务"><Square className="h-3.5 w-3.5" aria-hidden /></Button>}
        </div>
      </div>
      {task.error && <p className="line-clamp-2 break-all pl-10 text-xs leading-relaxed text-destructive">{task.error}</p>}
    </li>
  );
}

function StatusBadge({ status }: { status: QueueStatus }) {
  const tones: Record<QueueStatus, string> = {
    queued: "bg-ink/5 text-ink-muted",
    running: "bg-primary/10 text-primary-text",
    done: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
    stopped: "bg-ink/5 text-ink-subtle",
  };
  return <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${tones[status]}`}>{statusLabel[status]}</span>;
}