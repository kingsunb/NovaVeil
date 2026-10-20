/**
 * 上下文容量探测页：勾选渠道下的模型，指定一个目标规模，逐个探测上游能否接受。
 * 探测走与真实转发一致的出站构造（渠道配置 + 免费档改写 + Bun 传输）。
 */
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { ContextProbeResult } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Pill } from "@/components/ui/pill";

/** 可选的目标规模，与后端 contextProbeMaxTokens 上限保持一致。 */
const PROBE_SIZES = [
  { label: "128K", value: 131_072 },
  { label: "256K", value: 262_144 },
  { label: "500K", value: 512_000 },
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
  const [channelId, setChannelId] = useState<number | "">("");
  const [selected, setSelected] = useState<string[]>([]);
  const [tokens, setTokens] = useState<number>(131_072);
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<Record<string, ContextProbeResult>>({});

  const channels = channelsQuery.data ?? [];
  const currentChannel = useMemo(
    () => channels.find((c) => c.id === channelId),
    [channels, channelId],
  );
  const models = currentChannel?.models ?? [];

  function toggleModel(name: string) {
    setSelected((prev) =>
      prev.includes(name) ? prev.filter((item) => item !== name) : [...prev, name],
    );
  }

  async function runProbe() {
    if (channelId === "" || selected.length === 0) {
      toast.error("先选渠道与至少一个模型");
      return;
    }
    setRunning(true);
    setResults({});
    try {
      for (const model of selected) {
        try {
          const reply = await api.probeModelContext(Number(channelId), model, tokens);
          setResults((prev) => ({ ...prev, [model]: reply }));
        } catch (error) {
          const message = error instanceof Error ? error.message : "探测失败";
          setResults((prev) => ({
            ...prev,
            [model]: { tokens, accepted: false, status: 0, detail: message },
          }));
        }
      }
      toast.success("探测结束");
    } finally {
      setRunning(false);
    }
  }

  async function applyModel(model: string) {
    try {
      await api.applyModelContext(Number(channelId), model, tokens);
      toast.success(`已把 ${model} 的上下文窗口写为 ${formatTokens(tokens)}`);
      await queryClient.invalidateQueries({ queryKey: ["channels"] });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "写入失败");
    }
  }

  const rows = Object.entries(results);

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>上下文探测</CardTitle>
          <CardDescription>
            勾选模型并选定目标规模，逐个探测上游能否接受。探测消耗上游额度，付费渠道按输入 token 计费。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <Field label="渠道">
              <select
                className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm"
                value={channelId}
                onChange={(e) => {
                  setChannelId(e.target.value ? Number(e.target.value) : "");
                  setSelected([]);
                  setResults({});
                }}
              >
                <option value="">选择渠道</option>
                {channels.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="目标规模">
              <div className="flex flex-wrap gap-2">
                {PROBE_SIZES.map((option) => (
                  <Button
                    key={option.value}
                    size="sm"
                    variant={tokens === option.value ? "primary" : "ghost"}
                    onClick={() => setTokens(option.value)}
                  >
                    {option.label}
                  </Button>
                ))}
              </div>
            </Field>
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <p className="text-sm font-medium text-ink">模型</p>
              {models.length > 0 ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    setSelected(
                      selected.length === models.length ? [] : models.map((m) => m.name),
                    )
                  }
                >
                  {selected.length === models.length ? "清空" : "全选"}
                </Button>
              ) : null}
            </div>
            {models.length === 0 ? (
              <p className="text-xs text-ink-muted">该渠道还没有模型，先同步或在编辑器里添加。</p>
            ) : (
              <div className="grid max-h-56 grid-cols-2 gap-1 overflow-auto rounded-md border border-border bg-surface-subtle/40 p-3">
                {models.map((m) => (
                  <label key={m.name} className="flex items-center gap-2 text-sm text-ink">
                    <input
                      type="checkbox"
                      checked={selected.includes(m.name)}
                      onChange={() => toggleModel(m.name)}
                    />
                    <span className="mono truncate">{m.name}</span>
                  </label>
                ))}
              </div>
            )}
          </div>

          <div className="flex items-center gap-2">
            <Button onClick={runProbe} disabled={running || selected.length === 0}>
              {running ? "探测中" : `探测 ${formatTokens(tokens)}`}
            </Button>
            <span className="text-xs text-ink-muted">
              已选 {selected.length} 个模型，逐个串行探测
            </span>
          </div>
        </CardContent>
      </Card>

      {rows.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>探测结果</CardTitle>
            <CardDescription>只有成功接受的模型才可以写回上下文窗口。</CardDescription>
          </CardHeader>
          <CardContent>
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-ink-muted">
                  <th className="py-2">模型</th>
                  <th className="py-2">规模</th>
                  <th className="py-2">结果</th>
                  <th className="py-2">说明</th>
                  <th className="py-2" />
                </tr>
              </thead>
              <tbody>
                {rows.map(([model, reply]) => (
                  <tr key={model} className="border-t border-border">
                    <td className="mono max-w-[18rem] truncate py-2">{model}</td>
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
                    <td className="max-w-[20rem] truncate py-2 text-xs text-ink-muted">
                      {reply.status === 0 ? reply.detail : `HTTP ${reply.status}`}
                    </td>
                    <td className="py-2 text-right">
                      {reply.accepted ? (
                        <Button size="sm" variant="ghost" onClick={() => applyModel(model)}>
                          应用
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
    </div>
  );
}
