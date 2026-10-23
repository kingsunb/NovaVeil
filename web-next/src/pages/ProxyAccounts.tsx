/**
 * OpenCode 账号体检页：对免费档渠道的代理账号池并发体检。
 * 每个账号是一次与转发同构的极小真实请求：200→健康入池优先复用；
 * 403/429→冷却 24 小时；其余状态为失败（基础设施问题，不冷却）。
 */
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { UsersRound } from "lucide-react";

import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { zhCN } from "@/locales/zh-CN";

/** 与后端 ocProbe* 常量对齐。 */
const MAX_ACCOUNTS = 200;
const MAX_CONCURRENCY = 20;
const DEFAULT_COUNT = 100;
const DEFAULT_CONCURRENCY = 10;

interface AccountProbeResult {
  account: string;
  outcome: string;
  status: number;
  detail: string;
  latency_ms: number;
}

interface AccountProbeReport {
  channel_id: number;
  model: string;
  results: AccountProbeResult[];
  summary: { total: number; healthy: number; rejected: number; failed: number };
}

function outcomeTone(outcome: string): "success" | "danger" | "warning" | "neutral" {
  if (outcome === "healthy") return "success";
  if (outcome === "rejected") return "danger";
  if (outcome === "error") return "warning";
  return "neutral";
}

function outcomeLabel(outcome: string): string {
  if (outcome === "healthy") return zhCN.proxyAccounts.outcomeHealthy;
  if (outcome === "rejected") return zhCN.proxyAccounts.outcomeRejected;
  if (outcome === "error") return zhCN.proxyAccounts.outcomeFailed;
  return outcome;
}

export default function ProxyAccountsPage() {
  const channelsQuery = useQuery({ queryKey: ["channels"], queryFn: () => api.listChannels() });
  const eligible = useMemo(
    () =>
      (channelsQuery.data ?? []).filter(
        (channel) =>
          channel.enabled &&
          channel.opencode_compat &&
          !!channel.channel_proxy &&
          channel.channel_proxy.includes("{account}") &&
          (channel.base_url?.includes("/zen") || channel.base_url?.includes("/inference")),
      ),
    [channelsQuery.data],
  );

  const [channelId, setChannelId] = useState<number | null>(null);
  const [count, setCount] = useState(String(DEFAULT_COUNT));
  const [concurrency, setConcurrency] = useState(String(DEFAULT_CONCURRENCY));
  const [customAccounts, setCustomAccounts] = useState("");
  const [running, setRunning] = useState(false);
  const [report, setReport] = useState<AccountProbeReport | null>(null);

  const selectedChannelId = channelId ?? eligible[0]?.id ?? null;
  const selectedChannel = eligible.find((channel) => channel.id === selectedChannelId) ?? null;

  async function run() {
    if (!selectedChannelId) {
      toast.error(zhCN.proxyAccounts.eligibleEmpty);
      return;
    }
    const parsedCount = Math.floor(Number(count));
    if (!Number.isFinite(parsedCount) || parsedCount < 1 || parsedCount > MAX_ACCOUNTS) {
      toast.error(zhCN.proxyAccounts.countInvalid);
      return;
    }
    const parsedConcurrency = Math.floor(Number(concurrency));
    if (!Number.isFinite(parsedConcurrency) || parsedConcurrency < 1 || parsedConcurrency > MAX_CONCURRENCY) {
      toast.error(zhCN.proxyAccounts.concurrencyInvalid);
      return;
    }
    const accounts = customAccounts
      .split(/[\s,;，；]+/)
      .map((item) => item.trim())
      .filter(Boolean);

    setRunning(true);
    try {
      const next = await api.probeProxyAccounts({
        channel_id: selectedChannelId,
        accounts: accounts.length > 0 ? accounts : undefined,
        count: accounts.length > 0 ? undefined : parsedCount,
        concurrency: parsedConcurrency,
      });
      setReport(next);
      toast.success(
        zhCN.proxyAccounts.runDone(
          next.summary.healthy,
          next.summary.rejected,
          next.summary.failed,
        ),
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.requestFailed);
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="space-y-4 p-4">
      <Card>
        <CardHeader>
          <CardTitle>{zhCN.proxyAccounts.title}</CardTitle>
          <CardDescription>{zhCN.proxyAccounts.description}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {eligible.length === 0 ? (
            <EmptyState
              icon={<UsersRound className="size-8" />}
              title={zhCN.proxyAccounts.eligibleEmpty}
            />
          ) : (
            <div className="grid gap-4 md:grid-cols-2">
              <Field label={zhCN.proxyAccounts.channel} hint={zhCN.proxyAccounts.channelHint}>
                <select
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                  value={selectedChannelId ?? ""}
                  onChange={(event) => setChannelId(Number(event.target.value))}
                >
                  {eligible.map((channel) => (
                    <option key={channel.id} value={channel.id}>
                      {channel.name}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={zhCN.proxyAccounts.concurrency} hint={zhCN.proxyAccounts.concurrencyHint}>
                <Input
                  value={concurrency}
                  onChange={(event) => setConcurrency(event.target.value)}
                  inputMode="numeric"
                />
              </Field>
              <Field label={zhCN.proxyAccounts.count}>
                <Input
                  value={count}
                  onChange={(event) => setCount(event.target.value)}
                  inputMode="numeric"
                  disabled={customAccounts.trim().length > 0}
                />
              </Field>
              <Field label={zhCN.proxyAccounts.customAccounts}>
                <textarea
                  className="min-h-20 w-full rounded-md border bg-transparent px-3 py-2 text-sm"
                  placeholder={zhCN.proxyAccounts.customAccountsPlaceholder}
                  value={customAccounts}
                  onChange={(event) => setCustomAccounts(event.target.value)}
                />
              </Field>
            </div>
          )}
          {eligible.length > 0 && (
            <Button onClick={() => void run()} disabled={running}>
              {running ? zhCN.proxyAccounts.running : zhCN.proxyAccounts.run}
            </Button>
          )}
        </CardContent>
      </Card>

      {report && (
        <Card>
          <CardHeader>
            <CardTitle>
              {zhCN.proxyAccounts.summaryTotal} {report.summary.total}
            </CardTitle>
            <CardDescription>{zhCN.proxyAccounts.modelLabel(report.model)}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Pill tone="success">{zhCN.proxyAccounts.summaryHealthy} {report.summary.healthy}</Pill>
              <Pill tone="danger">{zhCN.proxyAccounts.summaryRejected} {report.summary.rejected}</Pill>
              <Pill tone="warning">{zhCN.proxyAccounts.summaryFailed} {report.summary.failed}</Pill>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnAccount}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnOutcome}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnStatus}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnLatency}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnDetail}</th>
                  </tr>
                </thead>
                <tbody>
                  {report.results.map((result) => (
                    <tr key={result.account} className="border-b last:border-0">
                      <td className="px-2 py-2 font-mono">{result.account}</td>
                      <td className="px-2 py-2">
                        <Pill tone={outcomeTone(result.outcome)}>{outcomeLabel(result.outcome)}</Pill>
                      </td>
                      <td className="px-2 py-2">{result.status > 0 ? result.status : "-"}</td>
                      <td className="px-2 py-2">{zhCN.proxyAccounts.latencyMS(result.latency_ms)}</td>
                      <td
                        className="max-w-80 truncate px-2 py-2 text-muted-foreground"
                        title={`${result.detail}｜${zhCN.proxyAccounts.detailHint}`}
                      >
                        {result.detail}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
