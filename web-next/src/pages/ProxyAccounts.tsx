/**
 * OpenCode 账号体检页：对免费档渠道的代理账号池并发体检。
 * 每个账号是一次与转发同构的极小真实请求：200→健康入池优先复用；
 * 403/429→冷却 24 小时；其余状态为失败（基础设施问题，不冷却）。
 */
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { UsersRound } from "lucide-react";

import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmButton } from "@/components/ui/confirm-button";
import { EmptyState } from "@/components/ui/empty-state";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { Switch } from "@/components/ui/switch";
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

interface LedgerItem {
  account: string;
  state: string;
  outcome: string;
  last_probe_status: number;
  last_probe_at: string;
  last_success_at: string;
  cooldown_until: string;
  source: string;
}

interface Ledger {
  channel_id: number;
  items: LedgerItem[];
  total: number;
  usable: number;
  untested: number;
  unusable: number;
}

type LedgerFilter = "all" | "usable" | "untested" | "unusable";

function fmtTime(value: string): string {
  if (!value || value.startsWith("0001-")) return "-";
  return new Date(value).toLocaleString();
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
  const queryClient = useQueryClient();
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
  const [ledgerFilter, setLedgerFilter] = useState<LedgerFilter>("all");

  const ledgerQuery = useQuery({
    queryKey: ["proxy-accounts", selectedChannelId, ledgerFilter],
    queryFn: () => api.listProxyAccounts(selectedChannelId!, ledgerFilter),
    enabled: !!selectedChannelId,
  });
  const autoProbeQuery = useQuery({
    queryKey: ["proxy-accounts-auto-probe"],
    queryFn: () => api.getOpencodeAccountAutoProbe(),
  });

  async function toggleAutoProbe(enabled: boolean) {
    try {
      await api.setOpencodeAccountAutoProbe(enabled);
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts-auto-probe"] });
      toast.success(zhCN.proxyAccounts.autoProbeSaved(enabled));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.autoProbeFailed);
    }
  }

  async function deleteAccounts(accounts: string[]) {
    if (!selectedChannelId) return;
    try {
      const result = await api.deleteProxyAccounts(selectedChannelId, accounts);
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts"] });
      toast.success(zhCN.proxyAccounts.deleteDone(result.deleted));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.deleteFailed);
    }
  }

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
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts"] });
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
      {selectedChannelId && (
        <Card>
          <CardHeader>
            <CardTitle>{zhCN.proxyAccounts.autoProbeTitle}</CardTitle>
            <CardDescription>{zhCN.proxyAccounts.autoProbeDescription}</CardDescription>
          </CardHeader>
          <CardContent className="flex items-center gap-3">
            <Switch
              checked={autoProbeQuery.data ?? false}
              onCheckedChange={(checked) => void toggleAutoProbe(checked)}
              disabled={autoProbeQuery.isLoading}
            />
            <span className="text-sm">
              {autoProbeQuery.data ? zhCN.proxyAccounts.autoProbeOn : zhCN.proxyAccounts.autoProbeOff}
            </span>
          </CardContent>
        </Card>
      )}

      {selectedChannelId && (
        <Card>
          <CardHeader>
            <CardTitle>{zhCN.proxyAccounts.ledgerTitle}</CardTitle>
            <CardDescription>{zhCN.proxyAccounts.ledgerDescription}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap gap-2">
              {(
                [
                  ["all", zhCN.proxyAccounts.filterAll, ledgerQuery.data?.total ?? 0, "neutral"],
                  ["usable", zhCN.proxyAccounts.filterUsable, ledgerQuery.data?.usable ?? 0, "success"],
                  ["untested", zhCN.proxyAccounts.filterUntested, ledgerQuery.data?.untested ?? 0, "info"],
                  ["unusable", zhCN.proxyAccounts.filterUnusable, ledgerQuery.data?.unusable ?? 0, "danger"],
                ] as const
              ).map(([value, label, count, tone]) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => setLedgerFilter(value)}
                  className="focus-visible:outline-none"
                >
                  <Pill tone={ledgerFilter === value ? tone : "neutral"} dot={false}>
                    {label} {count}
                  </Pill>
                </button>
              ))}
            </div>
            {(ledgerQuery.data?.items?.length ?? 0) === 0 ? (
              <EmptyState icon={<UsersRound className="size-8" />} title={zhCN.proxyAccounts.emptyLedger} />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b text-left text-muted-foreground">
                      <th className="px-2 py-2">{zhCN.proxyAccounts.columnAccount}</th>
                      <th className="px-2 py-2">{zhCN.proxyAccounts.columnOutcome}</th>
                      <th className="px-2 py-2">{zhCN.proxyAccounts.filterAll}</th>
                      <th className="px-2 py-2">{zhCN.proxyAccounts.columnLastSuccess}</th>
                      <th className="px-2 py-2">{zhCN.proxyAccounts.columnSource}</th>
                      <th className="px-2 py-2" />
                    </tr>
                  </thead>
                  <tbody>
                    {(ledgerQuery.data?.items ?? []).map((item) => (
                      <tr key={item.account} className="border-b last:border-0">
                        <td className="px-2 py-2 font-mono">{item.account}</td>
                        <td className="px-2 py-2">
                          <Pill tone={item.state === "usable" ? "success" : item.state === "unusable" ? "danger" : "info"}>
                            {item.state === "usable"
                              ? zhCN.proxyAccounts.stateUsable
                              : item.state === "unusable"
                                ? zhCN.proxyAccounts.stateUnusable
                                : zhCN.proxyAccounts.stateUntested}
                          </Pill>
                          {item.cooldown_until && !item.cooldown_until.startsWith("0001-") && (
                            <span className="ml-1 text-xs text-muted-foreground">
                              {zhCN.proxyAccounts.cooldownUntil(fmtTime(item.cooldown_until))}
                            </span>
                          )}
                        </td>
                        <td className="px-2 py-2">
                          {item.outcome
                            ? `${outcomeLabel(item.outcome)}·${item.last_probe_status || "-"}`
                            : zhCN.proxyAccounts.neverProbed}
                          <span className="ml-1 text-xs text-muted-foreground">{fmtTime(item.last_probe_at)}</span>
                        </td>
                        <td className="px-2 py-2">{fmtTime(item.last_success_at)}</td>
                        <td className="px-2 py-2">
                          {item.source === "generated"
                            ? zhCN.proxyAccounts.sourceGenerated
                            : item.source === "traffic"
                              ? zhCN.proxyAccounts.sourceTraffic
                              : zhCN.proxyAccounts.sourceLegacy}
                        </td>
                        <td className="px-2 py-2 text-right">
                          <ConfirmButton onConfirm={() => void deleteAccounts([item.account])} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
