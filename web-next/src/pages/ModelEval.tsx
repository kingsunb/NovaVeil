import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { FlaskConical, ListChecks, ListOrdered, Play, Plus, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { EVAL_PROMPT, type EvalStatsSummary, type EvalTarget } from "@/lib/model-eval";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { EvalDetail } from "@/components/model-eval/EvalDetail";
import { EvalHistory } from "@/components/model-eval/EvalHistory";
import { EvalQueue } from "@/components/model-eval/EvalQueue";
import { EvalRanking } from "@/components/model-eval/EvalRanking";
import { EvalSelection } from "@/components/model-eval/EvalSelection";

type EvalView = "current" | "ranking" | "queue" | "history";

/** 「当前评估」视图右侧的工作流引导，三步说明与队列/排序视图形成递进。 */
const FLOW_STEPS = [
  { title: "选择模型", hint: "左侧按渠道展开，勾选一个或多个模型" },
  { title: "入队执行", hint: "点「开始评估」，任务按顺序自动执行并保存" },
  { title: "回看排序", hint: "到「评估历史」回看与重测，「评估排序」对比质量" },
] as const;

export default function ModelEvalPage() {
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const rawChannelId = Number(params.get("channel"));
  const channelId = Number.isSafeInteger(rawChannelId) && rawChannelId > 0 ? rawChannelId : 0;
  const modelName = params.get("model") ?? "";
  const viewParam = params.get("view");
  const view: EvalView = viewParam === "current" || viewParam === "ranking" || viewParam === "queue" || viewParam === "history" ? (viewParam as EvalView) : "history";
  const channelsQuery = useQuery({ queryKey: ["channels"], queryFn: api.listChannels });
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [detailId, setDetailId] = useState<number | null>(null);

  const allTargets = useMemo<EvalTarget[]>(() => {
    // 渠道按自定义排序（sort 降序、同值按名称兜底）排列，与渠道列表默认视图一致：
    // 优先级（sort 值）越高越靠上，避免「当前评估」里渠道顺序与渠道管理页不一致。
    const channels = [...(channelsQuery.data ?? [])].sort(
      (a, b) => (b.sort ?? 0) - (a.sort ?? 0) || a.name.localeCompare(b.name),
    );
    return channels.flatMap((channel) =>
      channel.enabled ? channel.models.map((model) => ({
        channelId: channel.id,
        channelName: channel.name,
        channelType: channel.type,
        channelModelId: model.id,
        modelName: model.name,
      })) : [],
    );
  }, [channelsQuery.data]);
  const scopeTargets = useMemo(() => allTargets.filter((target) => !channelId || target.channelId === channelId), [allTargets, channelId]);
  const selectedTargets = scopeTargets.filter((target) => selectedIds.has(target.channelModelId));

  const [bulkSettings, setBulkSettings] = useState({
    skipSuccess: false,
    skipFailure: false,
    onlyUntested: false,
  });
  const statsQuery = useQuery({
    queryKey: ["model-eval", "stats"],
    queryFn: ({ signal }) => api.listEvalStats(signal),
    staleTime: 0,
    refetchInterval: 5000,
  });
  const statsByTarget = useMemo(() => new Map<string, EvalStatsSummary>(
    (statsQuery.data?.items ?? []).map((stats) => [`${stats.channel_id}:${stats.model_name}`, stats] as const),
  ), [statsQuery.data]);
  const statsReady = statsQuery.isSuccess;
  /** 按评估设置过滤一键评估候选；onlyUntested 时只看从未评估过的模型。 */
  const filterBySettings = useMemo(() => (target: EvalTarget) => {
    const stats = statsByTarget.get(`${target.channelId}:${target.modelName}`);
    const totalCount = stats?.total_count ?? 0;
    const successCount = stats?.success_count ?? 0;
    const failureCount = totalCount - successCount;
    if (bulkSettings.onlyUntested) return totalCount === 0;
    if (bulkSettings.skipSuccess && successCount > 0) return false;
    if (bulkSettings.skipFailure && failureCount > 0) return false;
    return true;
  }, [bulkSettings, statsByTarget]);
  const freeTargets = useMemo(() => scopeTargets.filter((target) => {
    const channel = channelsQuery.data?.find((item) => item.id === target.channelId);
    return channel?.is_free === true;
  }), [channelsQuery.data, scopeTargets]);
  const freeCandidates = useMemo(() => freeTargets.filter(filterBySettings), [freeTargets, filterBySettings]);
  const untestedTargets = useMemo(() => scopeTargets.filter((target) => {
    const stats = statsByTarget.get(`${target.channelId}:${target.modelName}`);
    return (stats?.total_count ?? 0) === 0;
  }), [scopeTargets, statsByTarget]);

  const enqueueMut = useMutation({
    mutationFn: (channelModelIds: number[]) => api.enqueueEvals(channelModelIds),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["model-eval", "queue", "list"] });
      toast.success(`已加入 ${res.enqueued.length} 个评估任务，可在队列视图查看进度`);
      changeView("queue");
    },
    onError: (e: Error) => toast.error(e.message || "入队失败"),
  });

  function changeView(nextView: EvalView) {
    const next = new URLSearchParams(params);
    next.set("view", nextView);
    setParams(next, { replace: true });
  }

  function showHistory(nextChannelId: number, nextModelName = "") {
    const next = new URLSearchParams(params);
    next.set("view", "history");
    next.set("channel", String(nextChannelId));
    if (nextModelName) next.set("model", nextModelName);
    else next.delete("model");
    if (nextChannelId !== channelId) setSelectedIds(new Set());
    setParams(next, { replace: true });
  }

  function reuseResult(record: { id: number }) {
    setDetailId(null);
    void api.addEvalRankFromHistory(record.id).then((data) => {
      qc.setQueryData(["model-eval", "rank", "list"], data);
      changeView("ranking");
      toast.success("已加入排序");
    }).catch((e: Error) => toast.error(e.message || "加入排序失败"));
  }

  function repeatResult(record: { channel_model_id: number }) {
    setDetailId(null);
    enqueueMut.mutate([record.channel_model_id]);
  }

  const busy = enqueueMut.isPending;

  return (
    <div className="min-w-0 space-y-4 pb-4">
      <PageToolbar
        leading={<div className="flex items-center gap-2"><FlaskConical className="h-5 w-5 text-primary-text" aria-hidden /><h1 className="text-lg font-semibold tracking-tight text-ink">模型评估</h1></div>}
        trailing={view === "history" ? <Button type="button" size="sm" onClick={() => changeView("current")}><Plus className="h-3.5 w-3.5" aria-hidden />新评估</Button> : undefined}
      />
      <p className="text-xs leading-relaxed text-ink-muted">用同一道 SVG 动画题比较模型表现，按渠道保存每次结果，支持回看、重测和按质量排序。</p>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <SegmentedControl
          aria-label="评估视图"
          value={view}
          onChange={changeView}
          size="md"
          options={[
            { value: "current", label: "当前评估" },
            { value: "ranking", label: "评估排序" },
            { value: "queue", label: "评估队列" },
            { value: "history", label: "评估历史" },
          ]}
        />
        <label className="flex min-w-0 max-w-full items-center gap-2 text-xs text-ink-muted">
          <span className="shrink-0">渠道范围</span>
          <Select
            aria-label="渠道范围"
            className="min-w-0 max-w-[min(65vw,20rem)]"
            value={channelId}
            disabled={busy}
            onChange={(event) => {
              const next = new URLSearchParams(params);
              if (event.target.value === "0") next.delete("channel");
              else next.set("channel", event.target.value);
              next.delete("model");
              setSelectedIds(new Set());
              setParams(next, { replace: true });
            }}
          >
            <option value={0}>全部渠道</option>
            {(channelsQuery.data ?? []).map((channel) => <option key={channel.id} value={channel.id}>{channel.name}{channel.enabled ? "" : "（已停用）"}</option>)}
            {channelId > 0 && !channelsQuery.data?.some((channel) => channel.id === channelId) && <option value={channelId}>渠道 #{channelId}</option>}
          </Select>
        </label>
      </div>

      {channelsQuery.isError && <QueryErrorBanner onRetry={() => void channelsQuery.refetch()} />}

      {view === "history" ? (
        <EvalHistory
          key={`${channelId}:${modelName}`}
          channelId={channelId}
          modelName={modelName}
          targets={allTargets}
          busy={busy}
          onPreview={setDetailId}
          onReuse={reuseResult}
          onRepeat={repeatResult}
          onNew={() => changeView("current")}
          onClearModel={() => { const next = new URLSearchParams(params); next.delete("model"); setParams(next, { replace: true }); }}
        />
      ) : view === "ranking" ? (
        <EvalRanking busy={busy} onShowHistory={showHistory} />
      ) : view === "queue" ? (
        <EvalQueue busy={busy} />
      ) : (
        <div className="grid min-w-0 items-start gap-4 xl:grid-cols-[20rem_minmax(0,1fr)]">
          <div className="min-w-0 xl:sticky xl:top-0">
            {channelsQuery.isLoading ? <Skeleton className="h-80 w-full" /> : (
              <EvalSelection key={channelId} targets={scopeTargets} selectedIds={selectedIds} onSelectionChange={setSelectedIds} disabled={busy || channelsQuery.isError} onRun={() => enqueueMut.mutate(selectedTargets.map((t) => t.channelModelId))} onHistory={showHistory} />
            )}
          </div>
          <div className="min-w-0 space-y-4">
            <Card className="min-w-0 overflow-hidden">
              <div className="flex items-center gap-2 border-b border-border/50 p-4">
                <ListChecks className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden />
                <h2 className="text-sm font-semibold text-ink">一键评估</h2>
              </div>
              <div className="space-y-4 p-4 sm:p-5">
                <div className="grid gap-2 sm:grid-cols-2">
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={busy || !statsReady || freeCandidates.length === 0}
                    onClick={() => enqueueMut.mutate(freeCandidates.map((target) => target.channelModelId))}
                  >
                    <Play className="h-3.5 w-3.5" aria-hidden />
                    一键评估免费渠道所有模型{statsReady ? ` (${freeCandidates.length})` : ""}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={busy || !statsReady || untestedTargets.length === 0}
                    onClick={() => enqueueMut.mutate(untestedTargets.map((target) => target.channelModelId))}
                  >
                    <Play className="h-3.5 w-3.5" aria-hidden />
                    一键评估未评估模型{statsReady ? ` (${untestedTargets.length})` : ""}
                  </Button>
                </div>
                <div className="space-y-2 rounded-lg border border-border/50 bg-ink/[0.02] p-3">
                  <div className="flex items-center gap-2">
                    <SlidersHorizontal className="h-3.5 w-3.5 text-ink-muted" aria-hidden />
                    <h3 className="text-xs font-semibold text-ink">评估设置</h3>
                  </div>
                  <label className="flex cursor-pointer items-center gap-2 text-xs text-ink-muted">
                    <input
                      type="checkbox"
                      checked={bulkSettings.skipSuccess}
                      onChange={(event) => setBulkSettings((prev) => ({ ...prev, skipSuccess: event.target.checked }))}
                      className="h-3.5 w-3.5 shrink-0 accent-primary"
                    />
                    已评估成功模型不再评估
                  </label>
                  <label className="flex cursor-pointer items-center gap-2 text-xs text-ink-muted">
                    <input
                      type="checkbox"
                      checked={bulkSettings.skipFailure}
                      onChange={(event) => setBulkSettings((prev) => ({ ...prev, skipFailure: event.target.checked }))}
                      className="h-3.5 w-3.5 shrink-0 accent-primary"
                    />
                    已评估失败模型不再评估
                  </label>
                  <label className="flex cursor-pointer items-center gap-2 text-xs text-ink-muted">
                    <input
                      type="checkbox"
                      checked={bulkSettings.onlyUntested}
                      onChange={(event) => setBulkSettings((prev) => ({ ...prev, onlyUntested: event.target.checked }))}
                      className="h-3.5 w-3.5 shrink-0 accent-primary"
                    />
                    只评估未评估模型
                  </label>
                </div>
                <p className="text-[11px] leading-relaxed text-ink-subtle">两个按钮都按当前渠道范围筛选；「一键评估免费渠道所有模型」会额外遵守上面的评估设置，「一键评估未评估模型」始终只入队从未评估过的模型。</p>
              </div>
            </Card>
            <Card className="min-w-0 overflow-hidden">
              <div className="flex items-center gap-2 border-b border-border/50 p-4">
                <FlaskConical className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden />
                <h2 className="text-sm font-semibold text-ink">评估说明</h2>
              </div>
              <div className="p-4 sm:p-5">
                <h3 className="text-xs font-semibold tracking-tight text-ink">本次评估题目</h3>
                <p className="mt-2 max-w-2xl whitespace-pre-wrap break-words rounded-lg bg-ink/[0.03] p-3 text-[13px] leading-relaxed text-ink-muted">{EVAL_PROMPT}</p>
                <ol className="mt-5 grid gap-3 sm:grid-cols-3">
                  {FLOW_STEPS.map((step, index) => (
                    <li key={step.title} className="rounded-lg border border-border/50 bg-ink/[0.02] p-3">
                      <div className="flex items-center gap-2">
                        <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/[0.1] text-[11px] font-semibold tabular-nums text-primary-text" aria-hidden>{index + 1}</span>
                        <span className="text-[13px] font-medium text-ink">{step.title}</span>
                      </div>
                      <p className="mt-1.5 text-[11px] leading-relaxed text-ink-subtle">{step.hint}</p>
                    </li>
                  ))}
                </ol>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border/50 p-4">
                <p className="min-w-0 text-[11px] leading-relaxed text-ink-subtle">格式合规的成功结果会自动进入排序，其余结果保留在历史，便于重测与排查。</p>
                <Button type="button" variant="secondary" size="sm" onClick={() => changeView("queue")}>
                  <ListOrdered className="h-3.5 w-3.5" aria-hidden />查看评估队列
                </Button>
              </div>
            </Card>
          </div>
        </div>
      )}

      <EvalDetail id={detailId} onClose={() => setDetailId(null)} targets={allTargets} busy={busy} onReuse={reuseResult} onRepeat={repeatResult} />
    </div>
  );
}
