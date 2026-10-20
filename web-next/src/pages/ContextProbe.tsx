/**
 * 上下文容量探测页：搜索渠道或模型，勾选模型与目标规模后串行探测上游能否接受。
 * 探测走与真实转发一致的出站构造，并写入请求日志与探测历史。
 */
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { ContextProbeResult } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";

/** 可选的目标规模，上限与后端 contextProbeMaxTokens 一致。 */
const PROBE_SIZES = [
  { label: "64K", value: 65_536 },
  { label: "128K", value: 131_072 },
  { label: "256K", value: 262_144 },
  { label: "400K", value: 409_600 },
  { label: "500K", value: 512_000 },
  { label: "888K", value: 909_312 },
  { label: "980K", value: 1_003_520 },
  { label: "1M", value: 1_048_576 },
] as const;

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
}

export default function ContextProbePage() {
  const queryClient = useQueryClient();
  const channelsQuery = useQuery({ queryKey: ["channels"], queryFn: () => api.listChannels() });
  const [search, setSearch] = useState("");
  const [channelId, setChannelId] = useState<number | "">("");
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [selectedSizes, setSelectedSizes] = useState<number[]>([131_072]);
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<Record<string, ContextProbeResult>>({});
  const [applied, setApplied] = useState<Record<string, number>>({});

  const historyQuery = useQuery({
    queryKey: ["probe-history", channelId],
    queryFn: () => api.listContextProbeRecords(channelId === "" ? undefined : Number(channelId)),
  });

  const channels = useMemo(() => channelsQuery.data ?? [], [channelsQuery.data]);
  const keyword = search.trim().toLowerCase();
  const filteredChannels = useMemo(
    () => (keyword ? channels.filter((c) => c.name.toLowerCase().includes(keyword)) : channels),
    [channels, keyword],
  );
  const currentChannel = useMemo(
    () => channels.find((c) => c.id === channelId),
    [channels, channelId],
  );
  const models = useMemo(() => {
    const list = currentChannel?.models ?? [];
    return keyword ? list.filter((m) => m.name.toLowerCase().includes(keyword)) : list;
  }, [currentChannel, keyword]);

  function toggleModel(name: string) {
    setSelectedModels((prev) =>
      prev.includes(name) ? prev.filter((item) => item !== name) : [...prev, name],
    );
  }

  function toggleSize(value: number) {
    setSelectedSizes((prev) =>
      prev.includes(value) ? prev.filter((item) => item !== value) : [...prev, value],
    );
  }

  async function runProbe() {
    if (channelId === "" || selectedModels.length === 0 || selectedSizes.length === 0) {
      toast.error("先选渠道、模型与至少一个规模");
      return;
    }
    setRunning(true);
    setResults({});
    setApplied({});
    try {
      for (const model of selectedModels) {
        for (const size of selectedSizes) {
          const key = `${size}#${model}`;
          try {
            const reply = await api.probeModelContext(Number(channelId), model, size);
            setResults((prev) => ({ ...prev, [key]: reply }));
          } catch (error) {
            const message = error instanceof Error ? error.message : "探测失败";
            setResults((prev) => ({
              ...prev,
              [key]: { tokens: size, accepted: false, status: 0, detail: message, latency_ms: 0 },
            }));
          }
        }
      }
      await queryClient.invalidateQueries({ queryKey: ["probe-history"] });
      toast.success("探测结束");
    } finally {
      setRunning(false);
    }
  }

  async function applyResult(model: string, tokens: number) {
    try {
      await api.applyModelContext(Number(channelId), model, tokens);
      setApplied((prev) => ({ ...prev, [model]: tokens }));
      toast.success(`已把 ${model} 的上下文窗口写为 ${formatTokens(tokens)}`);
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "写入失败");
    }
  }

  const rows = useMemo(
    () =>
      Object.entries(results)
        // 规模在前, 第一个 # 即分隔符: 模型名可能含 @ 或 #, 不能用模型名做前缀。
        .map(([key, reply]) => ({ key, model: key.slice(key.indexOf("#") + 1), reply }))
        .sort((a, b) => a.model.localeCompare(b.model) || a.reply.tokens - b.reply.tokens),
    [results],
  );
  const history = historyQuery.data ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>上下文探测</CardTitle>
          <CardDescription>
            搜索渠道或模型，勾选模型与目标规模后逐个探测。探测消耗上游额度，付费渠道按输入 token 计费。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <Field label="搜索渠道 / 模型">
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="输入关键字过滤"
              />
            </Field>
            <Field label="渠道">
              <select
                className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm"
                value={channelId}
                onChange={(e) => {
                  setChannelId(e.target.value ? Number(e.target.value) : "");
                  setSelectedModels([]);
                  setResults({});
                  setApplied({});
                }}
              >
                <option value="">选择渠道</option>
                {filteredChannels.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <Field label="目标规模（可多选）">
            <div className="flex flex-wrap gap-2">
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
            </div>
          </Field>

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <p className="text-sm font-medium text-ink">模型（已选 {selectedModels.length}）</p>
              {models.length > 0 ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    setSelectedModels(
                      selectedModels.length === models.length ? [] : models.map((m) => m.name),
                    )
                  }
                >
                  {selectedModels.length === models.length ? "清空" : "全选当前列表"}
                </Button>
              ) : null}
            </div>
            {models.length === 0 ? (
              <p className="text-xs text-ink-muted">
                {currentChannel ? "没有匹配的模型，换个关键字或先同步模型。" : "先选择渠道。"}
              </p>
            ) : (
              <div className="grid max-h-56 grid-cols-2 gap-1 overflow-auto rounded-md border border-border bg-surface-subtle/40 p-3">
                {models.map((m) => (
                  <label key={m.name} className="flex items-center gap-2 text-sm text-ink">
                    <input
                      type="checkbox"
                      checked={selectedModels.includes(m.name)}
                      onChange={() => toggleModel(m.name)}
                    />
                    <span className="mono truncate">{m.name}</span>
                  </label>
                ))}
              </div>
            )}
          </div>

          <div className="flex items-center gap-2">
            <Button onClick={runProbe} disabled={running || selectedModels.length === 0}>
              {running ? "探测中" : `探测 ${selectedModels.length * selectedSizes.length} 组`}
            </Button>
            <span className="text-xs text-ink-muted">
              {selectedModels.length} 个模型 × {selectedSizes.length} 个规模，串行执行
            </span>
          </div>
        </CardContent>
      </Card>

      {rows.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>本次结果</CardTitle>
            <CardDescription>只有「接受」的规模可以写回上下文窗口。</CardDescription>
          </CardHeader>
          <CardContent>
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-ink-muted">
                  <th className="py-2">模型</th>
                  <th className="py-2">规模</th>
                  <th className="py-2">结果</th>
                  <th className="py-2">耗时</th>
                  <th className="py-2">说明</th>
                  <th className="py-2" />
                </tr>
              </thead>
              <tbody>
                {rows.map(({ key, model, reply }) => (
                  <tr key={key} className="border-t border-border">
                    <td className="mono max-w-[16rem] truncate py-2">{model}</td>
                    <td className="py-2">{formatTokens(reply.tokens)}</td>
                    <td className="py-2">
                      {reply.status === 0 ? (
                        <Pill tone="neutral">失败</Pill>
                      ) : reply.accepted ? (
                        <Pill tone="success">接受</Pill>
                      ) : (
                        <Pill tone="danger">拒绝</Pill>
                      )}
                    </td>
                    <td className="py-2 text-xs text-ink-muted">
                      {reply.latency_ms > 0 ? `${reply.latency_ms} ms` : "-"}
                    </td>
                    <td className="max-w-[18rem] truncate py-2 text-xs text-ink-muted">
                      {reply.status === 0 ? reply.detail : `HTTP ${reply.status}`}
                    </td>
                    <td className="py-2 text-right">
                      {reply.accepted ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => applyResult(model, reply.tokens)}
                        >
                          {applied[model] === reply.tokens ? "已应用" : "应用"}
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
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
                    <td className="py-2">{formatTokens(row.tokens)}</td>
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
