/**
 * 上下文容量探测页：像模型评估那样列出全部渠道与模型，勾选后按「模型 × 规模」排队探测。
 * 探测走与真实转发一致的出站构造（含出口 IP 池），并写入请求日志与探测历史。
 * 同一张列表还支持按「渠道 × 模型」直接定义/清除人工上下文窗口，无需先探测。
 */
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronDown, ChevronRight } from "lucide-react";

import { api } from "@/lib/api";
import {
  MAX_PROBE_TOKENS,
  PROBE_SIZES,
  formatExactTokens,
  formatWindowTokens,
} from "@/lib/context-probe";
import type { ContextProbeResult } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { zhCN } from "@/locales/zh-CN";

/** 队列并发数: 同一上游的免费渠道不宜压太狠, 3 路足够。 */
const PROBE_CONCURRENCY = 3;

interface ProbeTarget {
  key: string;
  channelId: number;
  channelName: string;
  /** 上游端点; 与后端密度缓存键「端点:模型」共用, 同键任务串行。 */
  endpoint: string;
  model: string;
  /** 渠道上人工定义的窗口; 0 表示未定义。只有人工值参与路由跳过。 */
  manualWindow: number;
  /** 同步目录带来的窗口; 仅展示, 不参与路由跳过。 */
  syncedWindow: number;
}

interface ProbeTask {
  key: string;
  channelId: number;
  channelName: string;
  endpoint: string;
  model: string;
  tokens: number;
  state: "pending" | "running" | "done";
  result?: ContextProbeResult;
}

export default function ContextProbePage() {
  const queryClient = useQueryClient();
  const channelsQuery = useQuery({ queryKey: ["channels"], queryFn: () => api.listChannels() });
  const [search, setSearch] = useState("");
  const [expanded, setExpanded] = useState<Record<number, boolean>>({});
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set());
  const [selectedSizes, setSelectedSizes] = useState<number[]>([128_000]);
  const [customSize, setCustomSize] = useState("");
  const [running, setRunning] = useState(false);
  const [tasks, setTasks] = useState<ProbeTask[]>([]);
  const [windowDrafts, setWindowDrafts] = useState<Record<string, string>>({});
  const [batchWindow, setBatchWindow] = useState("");
  const [savingWindows, setSavingWindows] = useState(false);

  const historyQuery = useQuery({
    queryKey: ["probe-history"],
    queryFn: () => api.listContextProbeRecords(),
  });

  const targets = useMemo<ProbeTarget[]>(() => {
    const list: ProbeTarget[] = [];
    for (const channel of channelsQuery.data ?? []) {
      const limits = channel.model_limits ?? {};
      for (const model of channel.models ?? []) {
        list.push({
          key: `${channel.id}:${model.name}`,
          channelId: channel.id,
          channelName: channel.name,
          endpoint: (channel.base_url || `channel:${channel.id}`).replace(/\/+$/, ""),
          model: model.name,
          manualWindow: limits[model.name]?.context_window ?? 0,
          syncedWindow: model.context_window ?? 0,
        });
      }
    }
    return list;
  }, [channelsQuery.data]);

  const visible = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) return targets;
    return targets.filter((target) =>
      `${target.channelName} ${target.model}`.toLowerCase().includes(keyword),
    );
  }, [targets, search]);

  const groups = useMemo(() => {
    const grouped = new Map<number, { channelId: number; channelName: string; items: ProbeTarget[] }>();
    for (const target of visible) {
      const group = grouped.get(target.channelId) ?? {
        channelId: target.channelId,
        channelName: target.channelName,
        items: [],
      };
      group.items.push(target);
      grouped.set(target.channelId, group);
    }
    return [...grouped.values()];
  }, [visible]);

  const customSizes = useMemo(
    () => selectedSizes.filter((value) => !PROBE_SIZES.some((item) => item.value === value)),
    [selectedSizes],
  );
  const selectedCount = targets.filter((target) => selectedKeys.has(target.key)).length;
  const doneCount = tasks.filter((task) => task.state === "done").length;
  const failedCount = tasks.filter(
    (task) => task.state === "done" && task.result && task.result.status === 0,
  ).length;

  function toggleKeys(keys: string[]) {
    setSelectedKeys((prev) => {
      const allSelected = keys.every((key) => prev.has(key));
      const next = new Set(prev);
      for (const key of keys) {
        if (allSelected) next.delete(key);
        else next.add(key);
      }
      return next;
    });
  }

  function toggleSize(value: number) {
    setSelectedSizes((prev) =>
      prev.includes(value) ? prev.filter((item) => item !== value) : [...prev, value],
    );
  }

  function addCustomSize() {
    const value = Math.floor(Number(customSize));
    if (!Number.isFinite(value) || value <= 0) {
      toast.error("请输入大于 0 的 token 数");
      return;
    }
    if (value > MAX_PROBE_TOKENS) {
      toast.error(`不能超过 ${formatWindowTokens(MAX_PROBE_TOKENS)}（${MAX_PROBE_TOKENS}）`);
      return;
    }
    setSelectedSizes((prev) => (prev.includes(value) ? prev : [...prev, value]));
    setCustomSize("");
  }

  function windowDraft(target: ProbeTarget): string {
    return windowDrafts[target.key] ?? (target.manualWindow > 0 ? String(target.manualWindow) : "");
  }

  function parseWindowValue(raw: string): number | null {
    const value = Number(raw.trim());
    if (!Number.isInteger(value) || value < 0) return null;
    return value;
  }

  async function persistWindow(target: ProbeTarget, value: number, submittedDraft: string) {
    await api.applyModelContext(target.channelId, target.model, value);
    setWindowDrafts((prev) => {
      if (prev[target.key] !== submittedDraft) return prev;
      const next = { ...prev };
      delete next[target.key];
      return next;
    });
  }

  async function saveWindow(target: ProbeTarget) {
    const raw = windowDraft(target);
    if (raw.trim() === "") {
      toast.error(zhCN.contextProbe.emptyWindow);
      return;
    }
    const value = parseWindowValue(raw);
    if (value === null || value <= 0) {
      toast.error(zhCN.contextProbe.invalidWindow);
      return;
    }
    setSavingWindows(true);
    try {
      await persistWindow(target, value, raw);
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
      toast.success(
        `${zhCN.contextProbe.windowSaved}：${target.model} ${formatWindowTokens(value)}`,
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.contextProbe.saveFailed);
    } finally {
      setSavingWindows(false);
    }
  }

  async function clearWindow(target: ProbeTarget) {
    const submittedDraft = windowDraft(target);
    setSavingWindows(true);
    try {
      await persistWindow(target, 0, submittedDraft);
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
      toast.success(`${zhCN.contextProbe.windowCleared}：${target.model}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.contextProbe.clearFailed);
    } finally {
      setSavingWindows(false);
    }
  }

  async function applyBatch(value: number) {
    const picked = targets.filter((target) => selectedKeys.has(target.key));
    if (picked.length === 0) {
      toast.error(zhCN.contextProbe.batchSelectFirst);
      return;
    }
    // 清除只处理已定义人工窗口的模型, 不对未定义项谎报"已清除"。
    const writeTargets =
      value > 0 ? picked : picked.filter((target) => target.manualWindow > 0);
    const skipped = picked.length - writeTargets.length;
    if (writeTargets.length === 0) {
      toast.info(zhCN.contextProbe.batchClearNone);
      return;
    }
    setSavingWindows(true);
    const failed: Array<{ name: string; reason: string }> = [];
    try {
      for (const target of writeTargets) {
        try {
          await persistWindow(target, value, windowDraft(target));
        } catch (error) {
          failed.push({
            name: `${target.channelName}/${target.model}`,
            reason:
              error instanceof Error
                ? error.message
                : value > 0
                  ? zhCN.contextProbe.saveFailed
                  : zhCN.contextProbe.clearFailed,
          });
        }
      }
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
    } finally {
      setSavingWindows(false);
    }
    if (failed.length > 0) {
      const details = failed.map((item) => `${item.name}（${item.reason}）`).join("、");
      toast.error(
        value > 0
          ? zhCN.contextProbe.batchPartialFailure(failed.length, details)
          : zhCN.contextProbe.batchClearPartialFailure(failed.length, details),
      );
      return;
    }
    toast.success(
      value > 0
        ? zhCN.contextProbe.batchDefined(writeTargets.length)
        : zhCN.contextProbe.batchCleared(writeTargets.length, skipped),
    );
  }

  function applyBatchDraft() {
    const value = parseWindowValue(batchWindow);
    if (value === null || value <= 0) {
      toast.error(zhCN.contextProbe.batchWindowInvalid);
      return;
    }
    void applyBatch(value);
  }

  async function runProbe() {
    if (selectedKeys.size === 0 || selectedSizes.length === 0) {
      toast.error("先勾选模型与至少一个规模");
      return;
    }
    const picked = targets.filter((target) => selectedKeys.has(target.key));
    // 档位升序: 同一模型串行时, 最终密度缓存来自固定顺序的最后一档。
    const sizes = [...selectedSizes].sort((a, b) => a - b);
    const plan: ProbeTask[] = [];
    for (const target of picked) {
      for (const size of sizes) {
        plan.push({
          key: `${target.key}#${size}`,
          channelId: target.channelId,
          channelName: target.channelName,
          endpoint: target.endpoint,
          model: target.model,
          tokens: size,
          state: "pending",
        });
      }
    }
    setTasks(plan);
    setRunning(true);
    try {
      // 全局三路并发; 同一「端点:模型」同一时刻只允许 1 个在途任务,
      // 避免多档密度校准按完成顺序互相覆盖。
      const pendingIndices = plan.map((_, index) => index);
      const inFlight = new Set<string>();
      const taskEndpointModel = (task: ProbeTask) => `${task.endpoint}:${task.model}`;
      const worker = async () => {
        while (pendingIndices.length > 0) {
          const position = pendingIndices.findIndex(
            (index) => !inFlight.has(taskEndpointModel(plan[index])),
          );
          if (position < 0) return;
          const [index] = pendingIndices.splice(position, 1);
          const task = plan[index];
          inFlight.add(taskEndpointModel(task));
          setTasks((prev) =>
            prev.map((item, i) => (i === index ? { ...item, state: "running" } : item)),
          );
          try {
            const reply = await api.probeModelContext(task.channelId, task.model, task.tokens);
            setTasks((prev) =>
              prev.map((item, i) => (i === index ? { ...item, state: "done", result: reply } : item)),
            );
          } catch (error) {
            const message = error instanceof Error ? error.message : "探测失败";
            setTasks((prev) =>
              prev.map((item, i) =>
                i === index
                  ? {
                      ...item,
                      state: "done",
                      result: {
                        tokens: task.tokens,
                        accepted: false,
                        status: 0,
                        detail: message,
                        latency_ms: 0,
                        actual_tokens: 0,
                      },
                    }
                  : item,
              ),
            );
          } finally {
            inFlight.delete(taskEndpointModel(task));
          }
        }
      };
      await Promise.all(
        Array.from({ length: Math.min(PROBE_CONCURRENCY, plan.length) }, () => worker()),
      );
      await queryClient.invalidateQueries({ queryKey: ["probe-history"] });
      toast.success("探测结束");
    } finally {
      setRunning(false);
    }
  }

  async function applyResult(task: ProbeTask) {
    try {
      await api.applyModelContext(task.channelId, task.model, task.tokens);
      toast.success(
        zhCN.contextProbe.appliedWindow(task.model, formatWindowTokens(task.tokens)),
      );
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.contextProbe.applyFailed);
    }
  }

  const history = historyQuery.data ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>上下文探测</CardTitle>
          <CardDescription>
            勾选任意渠道下的模型与目标规模，按队列逐路探测。请求规模按全渠道统一口径估算
            （ASCII 4 字符约 1 token、非 ASCII 每字 1 token；1K=1000、1M=1,000,000），
            实测值与标称偏差超过 10% 时自动按实测密度重测一次。接受的行会回显上游报告的
            实际 token 数。探测消耗上游额度，付费渠道按输入 token 计费。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Field label="目标规模（可多选，可自定义）">
            <div className="flex flex-wrap items-center gap-2">
              {PROBE_SIZES.map((option) => (
                <Button
                  key={option.value}
                  size="sm"
                  variant={selectedSizes.includes(option.value) ? "primary" : "ghost"}
                  onClick={() => toggleSize(option.value)}
                >
                  {option.label}
                </Button>
              ))}
              {customSizes.map((value) => (
                <Button
                  key={value}
                  size="sm"
                  variant="primary"
                  onClick={() => toggleSize(value)}
                >
                  {formatWindowTokens(value)}
                </Button>
              ))}
              <span className="flex items-center gap-1">
                <Input
                  type="number"
                  min={1}
                  max={MAX_PROBE_TOKENS}
                  value={customSize}
                  onChange={(e) => setCustomSize(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") addCustomSize();
                  }}
                  placeholder="自定义 token 数"
                  className="w-32"
                />
                <Button size="sm" variant="ghost" onClick={addCustomSize}>
                  加入
                </Button>
              </span>
            </div>
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={runProbe} disabled={running || selectedCount === 0}>
              {running ? "探测中" : `开始探测（${selectedCount * selectedSizes.length} 组）`}
            </Button>
            <span className="text-xs text-ink-muted">
              {zhCN.contextProbe.concurrencyHint(selectedCount, selectedSizes.length)}
            </span>
          </div>
        </CardContent>
      </Card>

      <Card className="min-w-0 overflow-hidden">
        <div className="space-y-3 border-b border-border/50 p-4">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-ink">选择模型与定义窗口</h2>
            <span className="text-xs tabular-nums text-ink-muted">
              已选 {selectedCount} / {targets.length}
            </span>
          </div>
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索渠道或模型"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Input
              type="number"
              min={1}
              value={batchWindow}
              onChange={(e) => setBatchWindow(e.target.value)}
              placeholder="批量窗口 token"
              aria-label="批量窗口 token"
              className="h-8 w-40 text-xs"
            />
            <Button
              size="sm"
              variant="secondary"
              disabled={savingWindows || selectedCount === 0}
              onClick={applyBatchDraft}
            >
              批量定义（{selectedCount}）
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={savingWindows || selectedCount === 0}
              onClick={() => void applyBatch(0)}
            >
              批量清除
            </Button>
            <span className="text-xs text-ink-muted">
              只认人工定义值；同步目录值仅展示，不参与路由跳过
            </span>
          </div>
        </div>
        <div className="max-h-[24rem] divide-y divide-border/40 overflow-auto">
          {groups.length === 0 ? (
            <p className="p-4 text-xs text-ink-muted">没有匹配的渠道或模型。</p>
          ) : (
            groups.map((group) => {
              const open = expanded[group.channelId] ?? Boolean(search.trim());
              const keys = group.items.map((item) => item.key);
              const checked = keys.filter((key) => selectedKeys.has(key)).length;
              return (
                <div key={group.channelId}>
                  <div className="flex items-center gap-2 px-4 py-2">
                    <button
                      type="button"
                      aria-label={open ? "折叠渠道" : "展开渠道"}
                      onClick={() => setExpanded((prev) => ({ ...prev, [group.channelId]: !open }))}
                      className="text-ink-muted hover:text-ink"
                    >
                      {open ? (
                        <ChevronDown className="h-4 w-4" aria-hidden />
                      ) : (
                        <ChevronRight className="h-4 w-4" aria-hidden />
                      )}
                    </button>
                    <label className="flex min-w-0 flex-1 items-center gap-2 text-sm font-medium text-ink">
                      <input
                        type="checkbox"
                        checked={checked === keys.length && keys.length > 0}
                        onChange={() => toggleKeys(keys)}
                        aria-label={`选择渠道 ${group.channelName}`}
                      />
                      <span className="truncate">{group.channelName}</span>
                    </label>
                    <span className="text-xs tabular-nums text-ink-muted">
                      {checked}/{keys.length}
                    </span>
                  </div>
                  {open ? (
                    <div className="space-y-1 pb-2 pl-12 pr-4">
                      {group.items.map((item) => (
                        <div key={item.key} className="flex min-w-0 items-center gap-1.5 text-sm text-ink">
                          <input
                            type="checkbox"
                            checked={selectedKeys.has(item.key)}
                            onChange={() => toggleKeys([item.key])}
                            aria-label={`选择 ${item.model}`}
                            className="h-3.5 w-3.5 shrink-0 rounded border-border accent-primary"
                          />
                          <span className="mono min-w-0 flex-1 truncate" title={item.model}>
                            {item.model}
                          </span>
                          {item.syncedWindow > 0 ? (
                            <span
                              className="shrink-0 text-[10px] tabular-nums text-ink-muted"
                              title={zhCN.contextProbe.syncedWindowTitle(
                                formatExactTokens(item.syncedWindow),
                              )}
                            >
                              {zhCN.contextProbe.syncedWindow}{" "}
                              {formatWindowTokens(item.syncedWindow)}
                            </span>
                          ) : null}
                          {item.manualWindow > 0 ? (
                            <span
                              className="shrink-0"
                              title={zhCN.contextProbe.definedWindowTitle(
                                formatExactTokens(item.manualWindow),
                              )}
                            >
                              <Pill tone="neutral" className="shrink-0 text-[10px]">
                                {zhCN.contextProbe.definedWindow}{" "}
                                {formatWindowTokens(item.manualWindow)}
                              </Pill>
                            </span>
                          ) : null}
                          <Input
                            type="number"
                            min={1}
                            value={windowDraft(item)}
                            onChange={(e) =>
                              setWindowDrafts((prev) => ({ ...prev, [item.key]: e.target.value }))
                            }
                            placeholder="窗口 token"
                            aria-label={`${item.model} 窗口 token`}
                            className="h-7 w-24 shrink-0 text-xs"
                          />
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7 shrink-0 px-2 text-xs"
                            disabled={savingWindows || windowDraft(item).trim() === ""}
                            title={
                              windowDraft(item).trim() === ""
                                ? zhCN.contextProbe.saveDisabledHint
                                : undefined
                            }
                            onClick={() => void saveWindow(item)}
                            aria-label={`保存 ${item.model} 窗口`}
                          >
                            保存
                          </Button>
                          {item.manualWindow > 0 ? (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 shrink-0 px-2 text-xs"
                              disabled={savingWindows}
                              onClick={() => void clearWindow(item)}
                              aria-label={`清除 ${item.model} 窗口`}
                            >
                              清除
                            </Button>
                          ) : null}
                        </div>
                      ))}
                    </div>
                  ) : null}
                </div>
              );
            })
          )}
        </div>
      </Card>

      {tasks.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>探测队列</CardTitle>
            <CardDescription>
              共 {tasks.length} 组，已完成 {doneCount} 组{failedCount > 0 ? `，失败 ${failedCount} 组` : ""}。
            </CardDescription>
          </CardHeader>
          <CardContent>
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-ink-muted">
                  <th className="py-2">渠道</th>
                  <th className="py-2">模型</th>
                  <th className="py-2">规模</th>
                  <th className="py-2">状态</th>
                  <th className="py-2">耗时</th>
                  <th className="py-2">说明</th>
                  <th className="py-2" />
                </tr>
              </thead>
              <tbody>
                {tasks.map((task) => {
                  const result = task.result;
                  return (
                    <tr key={task.key} className="border-t border-border">
                      <td className="max-w-[10rem] truncate py-2">{task.channelName}</td>
                      <td className="mono max-w-[14rem] truncate py-2">{task.model}</td>
                      <td className="py-2">
                        {formatWindowTokens(task.tokens)}
                        {result?.actual_tokens ? (
                          <span className="ml-1 text-xs text-ink-muted">
                            实 {formatWindowTokens(result.actual_tokens)}
                          </span>
                        ) : null}
                      </td>
                      <td className="py-2">
                        {task.state === "pending" ? (
                          <Pill tone="neutral">排队中</Pill>
                        ) : task.state === "running" ? (
                          <Pill tone="info">探测中</Pill>
                        ) : !result ? (
                          <Pill tone="neutral">失败</Pill>
                        ) : result.status === 0 ? (
                          <Pill tone="neutral">失败</Pill>
                        ) : result.accepted ? (
                          <Pill tone="success">接受</Pill>
                        ) : (
                          <Pill tone="danger">拒绝</Pill>
                        )}
                      </td>
                      <td className="py-2 text-xs text-ink-muted">
                        {result && result.latency_ms > 0 ? `${result.latency_ms} ms` : "-"}
                      </td>
                      <td
                        className={
                          result && result.status === 0
                            ? "max-w-[18rem] truncate py-2 text-xs text-red-600 dark:text-red-400"
                            : "max-w-[18rem] truncate py-2 text-xs text-ink-muted"
                        }
                        title={result?.detail || undefined}
                      >
                        {!result ? "-" : result.status === 0
                          ? result.detail
                          : `HTTP ${result.status}${result.detail ? ` · ${result.detail}` : ""}`}
                      </td>
                      <td className="py-2 text-right">
                        {result?.accepted ? (
                          <Button size="sm" variant="ghost" onClick={() => applyResult(task)}>
                            应用
                          </Button>
                        ) : null}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>探测历史</CardTitle>
          <CardDescription>最近 100 条记录，按时间倒序。</CardDescription>
        </CardHeader>
        <CardContent>
          {history.length === 0 ? (
            <p className="text-xs text-ink-muted">还没有探测记录。</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-ink-muted">
                  <th className="py-2">时间</th>
                  <th className="py-2">渠道</th>
                  <th className="py-2">模型</th>
                  <th className="py-2">规模</th>
                  <th className="py-2">结果</th>
                  <th className="py-2">耗时</th>
                  <th className="py-2">说明</th>
                </tr>
              </thead>
              <tbody>
                {history.map((row) => (
                  <tr key={row.id} className="border-t border-border">
                    <td className="py-2 text-xs text-ink-muted">
                      {new Date(row.created_at).toLocaleString()}
                    </td>
                    <td className="max-w-[10rem] truncate py-2">{row.channel_name}</td>
                    <td className="mono max-w-[14rem] truncate py-2">{row.model_name}</td>
                    <td className="py-2">
                      {formatWindowTokens(row.tokens)}
                      {row.actual_tokens ? (
                        <span className="ml-1 text-xs text-ink-muted">
                          实 {formatWindowTokens(row.actual_tokens)}
                        </span>
                      ) : null}
                    </td>
                    <td className="py-2">
                      {row.status === 0 ? (
                        <Pill tone="neutral">失败</Pill>
                      ) : row.accepted ? (
                        <Pill tone="success">接受</Pill>
                      ) : (
                        <Pill tone="danger">拒绝</Pill>
                      )}
                    </td>
                    <td className="py-2 text-xs text-ink-muted">
                      {row.latency_ms > 0 ? `${row.latency_ms} ms` : "-"}
                    </td>
                    <td
                      className="max-w-[18rem] truncate py-2 text-xs text-ink-muted"
                      title={row.detail || undefined}
                    >
                      {row.detail || "-"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
