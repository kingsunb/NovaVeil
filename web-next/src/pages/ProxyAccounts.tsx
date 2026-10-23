/**
 * OpenCode 账号体检页：下拉选择免费档渠道（Free 与 Old 分开记账），展示所选渠道的
 * 体检控制与账号台账。生成即落账长期保存，台账随体检与转发流量实时更新，支持三态
 * 筛选、逐账号删除与一键测试台账全部账号。可开启每 24 小时自动体检。
 */
import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { UsersRound } from "lucide-react";

import { api } from "@/lib/api";
import type { Channel } from "@/lib/types";
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
const MAX_ACCOUNTS = 2000;
const MAX_CONCURRENCY = 20;
const DEFAULT_COUNT = 100;
const DEFAULT_CONCURRENCY = 10;
/** 一键测试时分批调用体检接口的批大小。 */
const BULK_CHUNK_SIZE = 200;
/** 台账分页: 默认每页 100, 可选最大 5000。 */
const LEDGER_PAGE_SIZE_DEFAULT = 100;
const LEDGER_PAGE_SIZE_OPTIONS = [100, 200, 500, 1000, 5000];

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
  success_count: number;
  success_days: number;
}

interface Ledger {
  channel_id: number;
  items: LedgerItem[];
  total: number;
  usable: number;
  untested: number;
  unusable: number;
  probe_error: number;
}

type LedgerFilter = "all" | "usable" | "untested" | "unusable" | "error";

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

function ledgerStateLabel(state: string): string {
  if (state === "usable") return zhCN.proxyAccounts.stateUsable;
  if (state === "unusable") return zhCN.proxyAccounts.stateUnusable;
  if (state === "error") return zhCN.proxyAccounts.stateError;
  return zhCN.proxyAccounts.stateUntested;
}

function ledgerStateTone(state: string): "success" | "danger" | "warning" | "info" {
  if (state === "usable") return "success";
  if (state === "unusable") return "danger";
  if (state === "error") return "warning";
  return "info";
}

/** 单渠道视图: 生成/体检控制、结果与台账, Free 与 Old 互不混账。 */
function ChannelSection({ channel }: { channel: Channel }) {
  const queryClient = useQueryClient();
  const [count, setCount] = useState(String(DEFAULT_COUNT));
  const [concurrency, setConcurrency] = useState(String(DEFAULT_CONCURRENCY));
  const [modelName, setModelName] = useState(channel.models?.[0]?.name ?? "");
  const [running, setRunning] = useState(false);
  const [report, setReport] = useState<AccountProbeReport | null>(null);
  const [ledgerFilter, setLedgerFilter] = useState<LedgerFilter>("all");
  const [selectedAccounts, setSelectedAccounts] = useState<Set<string>>(new Set());
  const [bulk, setBulk] = useState<{ done: number; total: number } | null>(null);
  const [pageSize, setPageSize] = useState(LEDGER_PAGE_SIZE_DEFAULT);
  const [pageIndex, setPageIndex] = useState(1);

  const ledgerQuery = useQuery({
    queryKey: ["proxy-accounts", channel.id, ledgerFilter],
    queryFn: () => api.listProxyAccounts(channel.id, ledgerFilter),
  });
  const ledgerItems = ledgerQuery.data?.items ?? [];
  const ledgerTotalPages = Math.max(1, Math.ceil(ledgerItems.length / pageSize));
  const ledgerPage = Math.min(pageIndex, ledgerTotalPages);
  const ledgerPageItems = useMemo(
    () => ledgerItems.slice((ledgerPage - 1) * pageSize, ledgerPage * pageSize),
    [ledgerItems, ledgerPage, pageSize],
  );
  const visibleSelected = ledgerPageItems.filter((item) => selectedAccounts.has(item.account)).length;

  // 数据收缩(删除/筛选变更)时把页码拉回有效范围。
  useEffect(() => {
    if (pageIndex > ledgerTotalPages) setPageIndex(ledgerTotalPages);
  }, [ledgerTotalPages, pageIndex]);

  function parseInputs(): { count: number; concurrency: number } | null {
    const parsedCount = Math.floor(Number(count));
    if (!Number.isFinite(parsedCount) || parsedCount < 1 || parsedCount > MAX_ACCOUNTS) {
      toast.error(zhCN.proxyAccounts.countInvalid);
      return null;
    }
    const parsedConcurrency = Math.floor(Number(concurrency));
    if (
      !Number.isFinite(parsedConcurrency) ||
      parsedConcurrency < 1 ||
      parsedConcurrency > MAX_CONCURRENCY
    ) {
      toast.error(zhCN.proxyAccounts.concurrencyInvalid);
      return null;
    }
    return { count: parsedCount, concurrency: parsedConcurrency };
  }

  async function run() {
    const inputs = parseInputs();
    if (!inputs) return;
    setRunning(true);
    try {
      const next = await api.probeProxyAccounts({
        channel_id: channel.id,
        count: inputs.count,
        concurrency: inputs.concurrency,
        model: modelName || undefined,
      });
      setReport(next);
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts"] });
      toast.success(
        zhCN.proxyAccounts.runDone(next.summary.healthy, next.summary.rejected, next.summary.failed),
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.requestFailed);
    } finally {
      setRunning(false);
    }
  }

  /** 一键测试: 勾选了账号时只测勾选的, 否则测台账全部; 按 200 个一批顺序体检。 */
  async function runBulkTest() {
    const inputs = parseInputs();
    const concurrency = inputs?.concurrency ?? DEFAULT_CONCURRENCY;
    setBulk({ done: 0, total: 0 });
    try {
      const full = await api.listProxyAccounts(channel.id, "all");
      const ledgerAccounts = (full.items ?? []).map((item) => item.account);
      const accounts =
        selectedAccounts.size > 0
          ? ledgerAccounts.filter((account) => selectedAccounts.has(account))
          : ledgerAccounts;
      if (accounts.length === 0) {
        toast.info(zhCN.proxyAccounts.emptyLedger);
        setBulk(null);
        return;
      }
      setBulk({ done: 0, total: accounts.length });
      let healthy = 0;
      let rejected = 0;
      let failed = 0;
      let model = "";
      const results: AccountProbeResult[] = [];
      for (let index = 0; index < accounts.length; index += BULK_CHUNK_SIZE) {
        const chunk = accounts.slice(index, index + BULK_CHUNK_SIZE);
        const part = await api.probeProxyAccounts({
          channel_id: channel.id,
          accounts: chunk,
          concurrency,
          model: modelName || undefined,
        });
        model = part.model;
        healthy += part.summary.healthy;
        rejected += part.summary.rejected;
        failed += part.summary.failed;
        results.push(...part.results);
        setBulk({ done: results.length, total: accounts.length });
      }
      setReport({
        channel_id: channel.id,
        model,
        results,
        summary: { total: results.length, healthy, rejected, failed },
      });
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts"] });
      setSelectedAccounts(new Set());
      toast.success(zhCN.proxyAccounts.runDone(healthy, rejected, failed));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.requestFailed);
    } finally {
      setBulk(null);
    }
  }

  async function deleteAccounts(accounts: string[]) {
    try {
      const result = await api.deleteProxyAccounts(channel.id, accounts);
      await queryClient.invalidateQueries({ queryKey: ["proxy-accounts"] });
      toast.success(zhCN.proxyAccounts.deleteDone(result.deleted));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : zhCN.proxyAccounts.deleteFailed);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{channel.name}</CardTitle>
        <CardDescription>
          {report ? zhCN.proxyAccounts.modelLabel(report.model) : channel.base_url}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 md:grid-cols-3">
          <Field label={zhCN.proxyAccounts.model}>
            <select
              className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
              value={modelName}
              onChange={(event) => setModelName(event.target.value)}
            >
              {(channel.models ?? []).map((model) => (
                <option key={model.name} value={model.name}>
                  {model.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label={zhCN.proxyAccounts.count}>
            <Input value={count} onChange={(event) => setCount(event.target.value)} inputMode="numeric" />
          </Field>
          <Field label={zhCN.proxyAccounts.concurrency} hint={zhCN.proxyAccounts.concurrencyHint}>
            <Input
              value={concurrency}
              onChange={(event) => setConcurrency(event.target.value)}
              inputMode="numeric"
            />
          </Field>
        </div>
        <div>
          <Button onClick={() => void run()} disabled={running}>
            {running ? zhCN.proxyAccounts.running : zhCN.proxyAccounts.run}
          </Button>
        </div>

        {report && (
          <div className="space-y-3">
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
          </div>
        )}

        <div className="space-y-3 border-t pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">{zhCN.proxyAccounts.ledgerTitle}</span>
            {(
              [
                ["all", zhCN.proxyAccounts.filterAll, ledgerQuery.data?.total ?? 0, "neutral"],
                ["usable", zhCN.proxyAccounts.filterUsable, ledgerQuery.data?.usable ?? 0, "success"],
                ["untested", zhCN.proxyAccounts.filterUntested, ledgerQuery.data?.untested ?? 0, "info"],
                ["unusable", zhCN.proxyAccounts.filterUnusable, ledgerQuery.data?.unusable ?? 0, "danger"],
                ["error", zhCN.proxyAccounts.filterError, ledgerQuery.data?.probe_error ?? 0, "warning"],
              ] as const
            ).map(([value, label, count, tone]) => (
              <button
                key={value}
                type="button"
                onClick={() => {
                  setLedgerFilter(value);
                  setPageIndex(1);
                }}
                className="focus-visible:outline-none"
              >
                <Pill tone={ledgerFilter === value ? tone : "neutral"} dot={false}>
                  {label} {count}
                </Pill>
              </button>
            ))}
            <Button
              size="sm"
              variant="outline"
              className="ml-auto"
              onClick={() => void runBulkTest()}
              disabled={bulk !== null}
            >
              {bulk
                ? zhCN.proxyAccounts.bulkRunning(bulk.done, bulk.total)
                : selectedAccounts.size > 0
                  ? zhCN.proxyAccounts.bulkTestSelected(selectedAccounts.size)
                  : zhCN.proxyAccounts.bulkTest}
            </Button>
          </div>
          {(ledgerQuery.data?.items?.length ?? 0) === 0 ? (
            <EmptyState icon={<UsersRound className="size-8" />} title={zhCN.proxyAccounts.emptyLedger} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="px-2 py-2">
                      <input
                        type="checkbox"
                        aria-label={zhCN.proxyAccounts.selectAllVisible}
                        checked={ledgerPageItems.length > 0 && visibleSelected === ledgerPageItems.length}
                        onChange={(event) => {
                          const next = new Set(selectedAccounts);
                          for (const item of ledgerPageItems) {
                            if (event.target.checked) next.add(item.account);
                            else next.delete(item.account);
                          }
                          setSelectedAccounts(next);
                        }}
                      />
                    </th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnAccount}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnOutcome}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnSuccessCount}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnSuccessDays}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnLastSuccess}</th>
                    <th className="px-2 py-2">{zhCN.proxyAccounts.columnSource}</th>
                    <th className="px-2 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {(ledgerPageItems).map((item) => (
                    <tr key={item.account} className="border-b last:border-0">
                      <td className="px-2 py-2">
                        <input
                          type="checkbox"
                          aria-label={`${zhCN.proxyAccounts.selectAccount} ${item.account}`}
                          checked={selectedAccounts.has(item.account)}
                          onChange={(event) => {
                            const next = new Set(selectedAccounts);
                            if (event.target.checked) next.add(item.account);
                            else next.delete(item.account);
                            setSelectedAccounts(next);
                          }}
                        />
                      </td>
                      <td className="px-2 py-2 font-mono">{item.account}</td>
                      <td className="px-2 py-2">
                        <Pill tone={ledgerStateTone(item.state)}>{ledgerStateLabel(item.state)}</Pill>
                        {item.outcome ? (
                          <span className="ml-1 text-xs text-muted-foreground">
                            {outcomeLabel(item.outcome)}·{item.last_probe_status || "-"}{" "}
                            {fmtTime(item.last_probe_at)}
                          </span>
                        ) : (
                          <span className="ml-1 text-xs text-muted-foreground">
                            {zhCN.proxyAccounts.neverProbed}
                          </span>
                        )}
                        {item.cooldown_until && !item.cooldown_until.startsWith("0001-") && (
                          <span className="ml-1 text-xs text-muted-foreground">
                            {zhCN.proxyAccounts.cooldownUntil(fmtTime(item.cooldown_until))}
                          </span>
                        )}
                      </td>
                      <td className="px-2 py-2">{item.success_count}</td>
                      <td className="px-2 py-2">{item.success_days}</td>
                      <td className="px-2 py-2">{fmtTime(item.last_success_at)}</td>
                      <td className="px-2 py-2 text-right">
                        <ConfirmButton onConfirm={() => void deleteAccounts([item.account])} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {ledgerItems.length > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-muted-foreground">
              <div className="flex items-center gap-2">
                <span>{zhCN.proxyAccounts.pageSizeLabel}</span>
                <select
                  className="h-8 rounded-md border bg-transparent px-2 text-sm"
                  aria-label={zhCN.proxyAccounts.pageSizeLabel}
                  value={pageSize}
                  onChange={(event) => {
                    setPageSize(Number(event.target.value));
                    setPageIndex(1);
                  }}
                >
                  {LEDGER_PAGE_SIZE_OPTIONS.map((size) => (
                    <option key={size} value={size}>
                      {size}
                    </option>
                  ))}
                </select>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={ledgerPage <= 1}
                  onClick={() => setPageIndex(ledgerPage - 1)}
                >
                  {zhCN.proxyAccounts.prevPage}
                </Button>
                <span>{zhCN.proxyAccounts.pageOf(ledgerPage, ledgerTotalPages)}</span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={ledgerPage >= ledgerTotalPages}
                  onClick={() => setPageIndex(ledgerPage + 1)}
                >
                  {zhCN.proxyAccounts.nextPage}
                </Button>
              </div>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

export default function ProxyAccountsPage() {
  const queryClient = useQueryClient();
  const channelsQuery = useQuery({ queryKey: ["channels"], queryFn: () => api.listChannels() });
  const eligible = (channelsQuery.data ?? []).filter(
    (channel) =>
      channel.enabled &&
      channel.opencode_compat &&
      !!channel.channel_proxy &&
      channel.channel_proxy.includes("{account}") &&
      (channel.base_url?.includes("/zen") || channel.base_url?.includes("/inference")),
  );
  const [selectedChannelId, setSelectedChannelId] = useState<number | null>(null);
  const selectedChannel =
    eligible.find((channel) => channel.id === selectedChannelId) ?? eligible[0] ?? null;
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

  return (
    <div className="space-y-4 p-4">
      {eligible.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState
              icon={<UsersRound className="size-8" />}
              title={zhCN.proxyAccounts.eligibleEmpty}
            />
          </CardContent>
        </Card>
      ) : (
        <>
          <Card>
            <CardHeader>
              <CardTitle>{zhCN.proxyAccounts.channel}</CardTitle>
              <CardDescription>{zhCN.proxyAccounts.channelHint}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-wrap items-center gap-4">
              <Field label={zhCN.proxyAccounts.channel}>
                <select
                  className="h-9 w-full min-w-56 rounded-md border bg-transparent px-3 text-sm"
                  value={selectedChannel?.id ?? ""}
                  onChange={(event) => setSelectedChannelId(Number(event.target.value))}
                >
                  {eligible.map((channel) => (
                    <option key={channel.id} value={channel.id}>
                      {channel.name}
                    </option>
                  ))}
                </select>
              </Field>
              <div className="flex items-center gap-3">
                <Switch
                  checked={autoProbeQuery.data ?? false}
                  onCheckedChange={(checked) => void toggleAutoProbe(checked)}
                  disabled={autoProbeQuery.isLoading}
                />
                <span className="text-sm">
                  {autoProbeQuery.data
                    ? zhCN.proxyAccounts.autoProbeOn
                    : zhCN.proxyAccounts.autoProbeOff}
                </span>
              </div>
            </CardContent>
          </Card>
          {selectedChannel && <ChannelSection key={selectedChannel.id} channel={selectedChannel} />}
        </>
      )}
    </div>
  );
}
