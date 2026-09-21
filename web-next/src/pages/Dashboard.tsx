import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  KeyRound,
  Sigma,
  GaugeCircle,
  DollarSign,
  Clock,
} from "lucide-react";

import { api } from "@/lib/api";
import { cn, formatNumber } from "@/lib/utils";
import type { TokenTrendRange } from "@/lib/types";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Pill } from "@/components/ui/pill";
import { TokenTrendChart } from "@/components/charts/TokenTrendChart";
import { TokenBreakdownChart } from "@/components/charts/TokenBreakdownChart";
import { UsageHeatmap } from "@/components/charts/UsageHeatmap";
import { Button } from "@/components/ui/button";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { EmptyState } from "@/components/ui/empty-state";
import { SegmentedControl } from "@/components/ui/segmented-control";

type Range = TokenTrendRange;

// 趋势档位按钮文案; 与后端 ValidUsageRange 的 24h/7d/30d/1y/3y 对齐（不暴露 forever）。
const RANGE_LABELS: Record<Range, string> = {
  "24h": "24 小时",
  "7d": "7 天",
  "30d": "30 天",
  "1y": "1 年",
  "3y": "3 年",
};

// 各档位对应的 KPI hint 文案; KPI 卡片共享同一窗口口径, hint 随档位联动。
const RANGE_HINTS: Record<Range, string> = {
  "24h": "近 24 小时",
  "7d": "近 7 天",
  "30d": "近 30 天",
  "1y": "近 1 年",
  "3y": "近 3 年",
};

/**
 * 仪表盘 —— 参考 TokenArena 的 Usage 总览 / 详细指标 / 预计消耗 / 使用时长 / 热力图
 *  - 6 KPI（总请求 / 客户端 IP / 错误数 / Token 用量 / 预计消耗 / 使用时长），随趋势档位联动
 *  - Token 趋势（24h/7d/30d/1y/3y，真实分桶时序），默认 24h
 *  - Token 构成环形图（input/output/reasoning/cached 占比）
 *  - 使用热力图（GitHub 风格，每日 token 用量）
 *  - 模型 Top + 最近错误
 */
export default function DashboardPage() {
  // 默认 24h：KPI 与趋势图默认展示近 24 小时窗口。
  const [range, setRange] = useState<Range>("24h");
  const navigate = useNavigate();

  // KPI 随趋势档位联动：queryKey 含 range，切换档位即重新拉取对应窗口的统计口径。
  const {
    data: now,
    isLoading: loadingNow,
    isError: nowError,
    refetch: refetchNow,
  } = useQuery({
    queryKey: ["now-version", range],
    queryFn: () => api.getNowVersion(range),
    refetchInterval: 30_000,
  });

  const {
    data: trend,
    isLoading: loadingTrend,
    isError: trendError,
    refetch: refetchTrend,
  } = useQuery({
    queryKey: ["token-trends", range],
    queryFn: () => api.getTokenTrends(range),
  });

  const {
    data: heatmap,
    isLoading: loadingHeatmap,
    isError: heatmapError,
    refetch: refetchHeatmap,
  } = useQuery({
    queryKey: ["usage-heatmap", 365],
    queryFn: () => api.getUsageHeatmap(365),
  });

  const {
    data: recent,
    isLoading: loadingRecent,
    isError: recentError,
    refetch: refetchRecent,
  } = useQuery({
    queryKey: ["recent-errors", 5],
    queryFn: () => api.listErrorLogs(5),
  });

  const kpis = buildKpis(now, range);
  const top = (now?.tokens_by_model ?? [])
    .map((model) => ({
      ...model,
      total_tokens: model.input + model.output,
    }))
    .sort((a, b) => b.total_tokens - a.total_tokens)
    .slice(0, 8);

  return (
    <div className="space-y-5">
      {/* 总览拉取失败时 KPI 全是 "—"，容易被当成「没有调用」；显式提示可重试 */}
      {nowError && <QueryErrorBanner onRetry={() => refetchNow()} />}

      {/* ── KPI 卡片: 总请求 / 客户端 IP / 错误数 / Token 用量 / 预计消耗 / 使用时长 ── */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
        {kpis.map((k) => (
          <KpiCard key={k.label} {...k} loading={loadingNow} />
        ))}
      </div>

      {/* ── Token 趋势 + Token 构成 ── */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <div>
              <CardTitle>Token 用量趋势</CardTitle>
              <CardDescription>
                输入 {formatNumber(now?.total_tokens_input ?? 0)} · 输出{" "}
                {formatNumber(now?.total_tokens_output ?? 0)}
              </CardDescription>
            </div>
            <SegmentedControl
              aria-label="趋势时间范围"
              value={range}
              onChange={setRange}
              options={
                (["24h", "7d", "30d", "1y", "3y"] as Range[]).map(
                  (r) => ({
                    value: r,
                    label: RANGE_LABELS[r],
                  }),
                )
              }
            />
          </CardHeader>
          <CardContent>
            <div className="mb-2 flex items-center gap-3 text-xs text-ink-muted">
              <span className="flex items-center gap-1.5">
                <span className="h-1.5 w-1.5 rounded-full bg-[#60A5FA]" />
                输入 tokens
              </span>
              <span className="flex items-center gap-1.5">
                <span className="h-1.5 w-1.5 rounded-full bg-[#34D399]" />
                输出 tokens
              </span>
            </div>
            {loadingTrend ? (
              <div className="flex h-48 items-center justify-center gap-2">
                <span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" />
                <span className="text-xs text-ink-muted">加载趋势中</span>
              </div>
            ) : trendError ? (
              <QueryErrorBanner onRetry={() => refetchTrend()} />
            ) : (
              <TokenTrendChart range={range} data={trend ?? []} />
            )}
          </CardContent>
        </Card>

        {/* Token 构成环形图 */}
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Token 构成</CardTitle>
              <CardDescription>
                {RANGE_HINTS[range]} · input/output/reasoning/cache
              </CardDescription>
            </div>
          </CardHeader>
          <CardContent>
            {loadingNow ? (
              <div className="flex h-40 items-center justify-center gap-2">
                <span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" />
                <span className="text-xs text-ink-muted">加载中</span>
              </div>
            ) : (
              <TokenBreakdownChart
                input={now?.total_tokens_input ?? 0}
                output={now?.total_tokens_output ?? 0}
                reasoning={now?.reasoning_tokens ?? 0}
                cached={now?.cached_tokens ?? 0}
              />
            )}
          </CardContent>
        </Card>
      </div>

      {/* ── 使用热力图 ── */}
      <Card>
        <CardHeader>
          <div>
            <CardTitle>使用热力图</CardTitle>
            <CardDescription>近 365 天每日 token 用量</CardDescription>
          </div>
        </CardHeader>
        <CardContent>
          {loadingHeatmap ? (
            <div className="flex h-32 items-center justify-center gap-2">
              <span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" />
              <span className="text-xs text-ink-muted">加载热力图中</span>
            </div>
          ) : heatmapError ? (
            <QueryErrorBanner onRetry={() => refetchHeatmap()} />
          ) : (
            <UsageHeatmap data={heatmap ?? []} />
          )}
        </CardContent>
      </Card>

      {/* ── 模型 Top + 最近错误 ── */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <div>
              <CardTitle>模型用量 Top</CardTitle>
              <CardDescription>按 token 总量排序</CardDescription>
            </div>
          </CardHeader>
          <CardContent>
            {loadingNow ? (
              <div className="flex items-center justify-center gap-2 py-6">
                <span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" />
                <span className="text-xs text-ink-muted">加载中</span>
              </div>
            ) : nowError ? (
              <QueryErrorBanner onRetry={() => refetchNow()} />
            ) : top.length === 0 ? (
              <EmptyState
                className="py-8"
                title="暂无模型用量"
                hint="有请求经过中继后，这里会按模型汇总 token"
              />
            ) : (
              <ul className="space-y-2.5">
                {top.map((m, i) => {
                  const pct = Math.min(
                    100,
                    (m.total_tokens / (top[0]?.total_tokens || 1)) * 100,
                  );
                  return (
                    <li key={m.name} className="space-y-1">
                      <div className="flex items-center justify-between text-xs">
                        <span className="mono truncate text-ink">
                          {i + 1}. {m.name}
                        </span>
                        <span className="num text-ink-muted">
                          {formatNumber(m.total_tokens, {
                            notation: "compact",
                          })}
                        </span>
                      </div>
                      <div className="h-1 overflow-hidden rounded-full bg-surface-subtle">
                        <div
                          className="h-full rounded-full bg-primary/70"
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div>
              <CardTitle>最近错误</CardTitle>
              <CardDescription>持久化错误日志前 5 条</CardDescription>
            </div>
            <Button
              variant="link"
              size="sm"
              onClick={() => navigate("/logs")}
              className="h-auto p-0 text-xs"
            >
              查看全部 →
            </Button>
          </CardHeader>
          <CardContent>
            {loadingRecent ? (
              <div className="flex items-center justify-center gap-2 py-6">
                <span className="h-4 w-4 animate-spin rounded-full border-2 border-primary/30 border-t-primary" />
                <span className="text-xs text-ink-muted">加载中</span>
              </div>
            ) : recentError ? (
              <QueryErrorBanner onRetry={() => refetchRecent()} />
            ) : (recent?.length ?? 0) === 0 ? (
              <EmptyState
                className="py-8"
                title="暂无错误"
                hint="近期请求都顺利完成"
              />
            ) : (
              <ul className="divide-y divide-border">
                {recent!.map((e) => (
                  <li
                    key={e.id}
                    className="flex items-start justify-between gap-2 py-2 text-xs"
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5">
                        <Pill tone="danger">{e.err_class}</Pill>
                        {e.model && (
                          <span className="mono truncate text-ink-muted">
                            {e.model}
                          </span>
                        )}
                      </div>
                      <p className="mt-0.5 truncate text-ink">{e.err_brief}</p>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

interface Kpi {
  label: string;
  value: string;
  icon: React.ReactNode;
  hint?: string;
  tone?: "neutral" | "success" | "warning" | "danger" | "info" | "cost";
}

function buildKpis(
  latest: Awaited<ReturnType<typeof api.getNowVersion>> | undefined,
  range: Range,
): Kpi[] {
  // KPI 共享同一时间窗口口径，hint 随档位联动。
  const hint = RANGE_HINTS[range];

  // 格式化使用时长
  const formatDurationLabel = (ms: number) => {
    if (ms <= 0) return "—";
    const seconds = Math.floor(ms / 1000);
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
    const hours = Math.floor(minutes / 60);
    return `${hours}h ${minutes % 60}m`;
  };

  return [
    {
      label: "总请求",
      value: latest ? formatNumber(latest.total_requests) : "—",
      icon: <Sigma className="h-4 w-4" aria-hidden />,
      hint,
      tone: "neutral",
    },
    {
      label: "客户端 IP",
      value: latest ? formatNumber(latest.client_ip_count) : "—",
      icon: <KeyRound className="h-4 w-4" aria-hidden />,
      hint,
      tone: "success",
    },
    {
      label: "错误数",
      value: latest ? formatNumber(latest.error_count) : "—",
      icon: <AlertTriangle className="h-4 w-4" aria-hidden />,
      hint,
      tone: latest && latest.error_count > 0 ? "warning" : "neutral",
    },
    {
      label: "Token 用量",
      value: latest
        ? formatNumber(
            latest.total_tokens_input + latest.total_tokens_output,
            { notation: "compact" },
          )
        : "—",
      icon: <GaugeCircle className="h-4 w-4" aria-hidden />,
      hint,
      tone: "info",
    },
    {
      label: "预计消耗",
      value:
        latest && (latest.total_cost ?? 0) > 0
          ? `$${latest.total_cost!.toFixed(2)}`
          : "—",
      icon: <DollarSign className="h-4 w-4" aria-hidden />,
      hint,
      tone: "cost",
    },
    {
      label: "使用时长",
      value: latest
        ? formatDurationLabel(latest.total_duration_ms ?? 0)
        : "—",
      icon: <Clock className="h-4 w-4" aria-hidden />,
      hint,
      tone: "neutral",
    },
  ];
}

const TONE_BG: Record<NonNullable<Kpi["tone"]>, string> = {
  neutral: "bg-ink/[0.05] text-ink-muted",
  success: "bg-emerald-500/[0.10] text-emerald-600 dark:text-emerald-400",
  warning: "bg-amber-500/[0.10] text-amber-600 dark:text-amber-400",
  danger: "bg-red-500/[0.10] text-red-600 dark:text-red-400",
  info: "bg-blue-500/[0.10] text-blue-600 dark:text-blue-400",
  cost: "bg-violet-500/[0.10] text-violet-600 dark:text-violet-400",
};

function KpiCard({
  label,
  value,
  icon,
  hint,
  tone = "neutral",
  loading,
}: Kpi & { loading?: boolean }) {
  return (
    <Card>
      <CardContent className="space-y-2 p-4">
        <div className="flex items-center justify-between text-xs text-ink-muted">
          <span>{label}</span>
          <span
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-full",
              TONE_BG[tone],
            )}
          >
            {icon}
          </span>
        </div>
        <div className="num text-2xl font-semibold tracking-tight text-ink">
          {loading ? (
            <span className="inline-block h-6 w-16 animate-pulse rounded bg-surface-subtle" />
          ) : (
            value
          )}
        </div>
        {hint && <div className="text-[11px] text-ink-subtle">{hint}</div>}
      </CardContent>
    </Card>
  );
}
