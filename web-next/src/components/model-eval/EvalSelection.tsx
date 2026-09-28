import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, History, ListChecks, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { SearchField } from "@/components/ui/search-field";
import { api } from "@/lib/api";
import { AUTO_GROUP_NAME, type EvalTarget } from "@/lib/model-eval";

export function EvalSelection({
  targets,
  selectedIds,
  onSelectionChange,
  disabled,
  onRun,
  onManualAdd,
  onHistory,
}: {
  targets: EvalTarget[];
  selectedIds: Set<number>;
  onSelectionChange: (ids: Set<number>) => void;
  disabled: boolean;
  onRun: () => void;
  onManualAdd: () => void;
  onHistory: (channelId: number, modelName?: string) => void;
}) {
  const [search, setSearch] = useState("");
  const [expanded, setExpanded] = useState<Record<number, boolean>>({});
  const statsQuery = useQuery({
    queryKey: ["model-eval", "stats"],
    queryFn: ({ signal }) => api.listEvalStats(signal),
    staleTime: 0,
    refetchInterval: 5000,
  });
  const statsByTarget = useMemo(() => new Map((statsQuery.data?.items ?? []).map((stats) =>
    [`${stats.channel_id}:${stats.model_name}`, stats] as const,
  )), [statsQuery.data]);
  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    return targets.filter((target) => `${target.channelName} ${target.modelName}`.toLowerCase().includes(query));
  }, [targets, search]);
  const groups = useMemo(() => {
    const grouped = new Map<number, EvalTarget[]>();
    for (const target of visible) {
      const items = grouped.get(target.channelId) ?? [];
      items.push(target);
      grouped.set(target.channelId, items);
    }
    return [...grouped.values()];
  }, [visible]);
  const selectedCount = targets.filter((target) => selectedIds.has(target.channelModelId)).length;

  function toggle(ids: number[]) {
    const allSelected = ids.every((id) => selectedIds.has(id));
    const next = new Set(selectedIds);
    for (const id of ids) {
      if (allSelected) next.delete(id);
      else next.add(id);
    }
    onSelectionChange(next);
  }

  return (
    <Card className="min-w-0 overflow-hidden">
      <div className="space-y-3 border-b border-border/50 p-4">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-ink">选择模型</h2>
          <span className="text-xs tabular-nums text-ink-muted">已选 {selectedCount} / {targets.length}</span>
        </div>
        <SearchField
          value={search}
          onChange={setSearch}
          placeholder="搜索渠道或模型"
          aria-label="搜索可评估的模型"
          inputClassName="w-full"
        />
        <div className="flex items-center gap-3 text-xs">
          <button
            type="button"
            disabled={disabled || visible.length === 0}
            onClick={() => onSelectionChange(new Set([...selectedIds, ...visible.map((target) => target.channelModelId)]))}
            className="text-primary-text hover:underline disabled:opacity-40"
          >
            {search.trim() ? "全选搜索结果" : "全选当前范围"}
          </button>
          <button
            type="button"
            disabled={disabled || selectedCount === 0}
            onClick={() => onSelectionChange(new Set())}
            className="text-ink-muted hover:underline disabled:opacity-40"
          >
            清空选择
          </button>
        </div>
        {statsQuery.isError && <QueryErrorBanner onRetry={() => void statsQuery.refetch()} />}
      </div>

      <div className="max-h-[min(55vh,36rem)] overflow-y-auto p-2">
        {groups.length === 0 ? (
          <EmptyState
            title={targets.length === 0 ? "没有可评估的模型" : "没有匹配的模型"}
            hint={targets.length === 0 ? "请在渠道页启用渠道并添加模型。" : "试试其他渠道或模型名称。"}
            className="px-3 py-8"
          />
        ) : groups.map((items) => {
          const channel = items[0];
          const ids = items.map((target) => target.channelModelId);
          const count = ids.filter((id) => selectedIds.has(id)).length;
          const open = expanded[channel.channelId] ?? (groups.length === 1 || items.length <= 3 || search.trim().length > 0);
          return (
            <div key={channel.channelId} className="mb-1 rounded-lg bg-ink/[0.02]">
              <div className="flex items-center gap-2 p-2">
                <button
                  type="button"
                  onClick={() => setExpanded((prev) => ({ ...prev, [channel.channelId]: !open }))}
                  aria-label={`${open ? "折叠" : "展开"} ${channel.channelName}`}
                  aria-expanded={open}
                  className="shrink-0 rounded p-1 text-ink-muted hover:bg-ink/5"
                >
                  {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                </button>
                <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-xs font-medium text-ink">
                  <input
                    type="checkbox"
                    checked={count === ids.length}
                    ref={(element) => { if (element) element.indeterminate = count > 0 && count < ids.length; }}
                    onChange={() => toggle(ids)}
                    disabled={disabled}
                    className="h-3.5 w-3.5 shrink-0 accent-primary"
                  />
                  <span className="truncate" title={channel.channelName}>{channel.channelName}</span>
                  <span className="shrink-0 tabular-nums text-ink-subtle">{count}/{ids.length}</span>
                </label>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 shrink-0"
                  onClick={() => onHistory(channel.channelId)}
                  aria-label={`查看 ${channel.channelName} 的评估历史`}
                  title="渠道评估历史"
                >
                  <History className="h-3.5 w-3.5" aria-hidden />
                </Button>
              </div>
              {open && (
                <div className="space-y-1 px-2 pb-2 pl-9">
                  {items.map((target) => {
                    const stats = statsByTarget.get(`${target.channelId}:${target.modelName}`);
                    return (
                      <div key={target.channelModelId} className="flex items-center gap-1">
                        <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-2 py-1.5 text-xs text-ink-muted">
                          <input
                            type="checkbox"
                            checked={selectedIds.has(target.channelModelId)}
                            onChange={() => toggle([target.channelModelId])}
                            disabled={disabled}
                            className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-primary"
                          />
                          <span className="min-w-0 space-y-0.5">
                            <span className="block break-all font-mono leading-5">{target.modelName}</span>
                            {stats && stats.total_count > 0 && (
                              <span className="block text-[10px] leading-relaxed tabular-nums text-ink-subtle" title="成功次数仅统计格式合规的评估">
                                累计评估 {stats.total_count} 次，成功 {stats.success_count} 次
                              </span>
                            )}
                          </span>
                        </label>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7 shrink-0"
                          onClick={() => onHistory(target.channelId, target.modelName)}
                          aria-label={`查看 ${target.channelName} ${target.modelName} 的评估历史`}
                          title="模型评估历史"
                        >
                          <History className="h-3 w-3" aria-hidden />
                        </Button>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}
      </div>
      <div className="space-y-2 border-t border-border/50 p-4">
        <Button type="button" className="w-full" onClick={onRun} disabled={disabled || selectedCount === 0}>
          <Play className="h-3.5 w-3.5" aria-hidden />
          开始评估{selectedCount > 0 ? ` (${selectedCount})` : ""}
        </Button>
        <Button type="button" variant="secondary" className="w-full" onClick={onManualAdd} disabled={disabled || selectedCount === 0}>
          <ListChecks className="h-3.5 w-3.5" aria-hidden />
          直接加入排序（不评估）{selectedCount > 0 ? ` (${selectedCount})` : ""}
        </Button>
        <p className="text-[11px] leading-relaxed text-ink-subtle">「开始评估」对每个所选模型发送一次实际请求；「直接加入排序」跳过评估，把所选模型作为「手动加入」条目直接放入排序，可随后应用到 {AUTO_GROUP_NAME} 分组。</p>
      </div>
    </Card>
  );
}
