import { useState, useMemo } from "react";
import { formatNumber } from "@/lib/utils";
import type { UsageHeatmapPoint } from "@/lib/types";

/**
 * UsageHeatmap —— GitHub 风格的使用热力图。
 * 按周列排列每日 token 用量, 颜色深浅表示用量级别, hover 显示日期与详细数据。
 * 参考 TokenArena 的使用热力图功能, 用纯 SVG 实现, 无外部图表依赖。
 */
export function UsageHeatmap({
  data,
  weeks = 53,
}: {
  data: UsageHeatmapPoint[];
  weeks?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);

  // 计算颜色级别阈值
  const maxTokens = useMemo(() => {
    const max = Math.max(...data.map((d) => d.tokens), 1);
    return max;
  }, [data]);

  // 构建日期映射
  const dataMap = useMemo(() => {
    const map = new Map<string, UsageHeatmapPoint>();
    for (const d of data) map.set(d.date, d);
    return map;
  }, [data]);

  // 生成周列数据: 每列 7 天(周日→周六), 从 weeks 周前到今天
  const columns = useMemo(() => {
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    // 找到今天所在周的周日
    const todayDay = today.getDay();
    const thisSunday = new Date(today);
    thisSunday.setDate(today.getDate() - todayDay);

    const cols: { date: string; point: UsageHeatmapPoint | null }[][] = [];
    for (let w = weeks - 1; w >= 0; w--) {
      const col: { date: string; point: UsageHeatmapPoint | null }[] = [];
      for (let d = 0; d < 7; d++) {
        const date = new Date(thisSunday);
        date.setDate(thisSunday.getDate() - w * 7 + d);
        const dateStr = formatDate(date);
        // 只显示今天及之前的数据
        if (date > today) {
          col.push({ date: dateStr, point: null });
        } else {
          col.push({ date: dateStr, point: dataMap.get(dateStr) ?? null });
        }
      }
      cols.push(col);
    }
    return cols;
  }, [dataMap, weeks]);

  // 颜色级别: 0(无数据) → 1(最低) → 4(最高)
  const getLevel = (tokens: number) => {
    if (tokens <= 0) return 0;
    const ratio = tokens / maxTokens;
    if (ratio < 0.25) return 1;
    if (ratio < 0.5) return 2;
    if (ratio < 0.75) return 3;
    return 4;
  };

  const LEVEL_COLORS = [
    "fill-surface-subtle stroke-border", // level 0: 无数据
    "fill-emerald-500/20 stroke-emerald-500/30", // level 1
    "fill-emerald-500/40 stroke-emerald-500/50", // level 2
    "fill-emerald-500/65 stroke-emerald-500/75", // level 3
    "fill-emerald-500/90 stroke-emerald-500", // level 4
  ];

  const cellSize = 11;
  const cellGap = 2;
  const labelWidth = 24;
  const colWidth = cellSize + cellGap;
  const svgWidth = labelWidth + columns.length * colWidth;
  const svgHeight = 18 + 7 * colWidth; // 月份标签 + 7 行

  // 月份标签
  const monthLabels = useMemo(() => {
    const labels: { text: string; x: number }[] = [];
    let lastMonth = -1;
    columns.forEach((col, i) => {
      // 每列至少有一个 cell，取第一个 cell 的日期确定月份
      const firstCell = col[0];
      const month = new Date(firstCell.date).getMonth();
      if (month !== lastMonth) {
        labels.push({
          text: `${month + 1}月`,
          x: labelWidth + i * colWidth,
        });
        lastMonth = month;
      }
    });
    return labels;
  }, [columns, colWidth]);

  const weekdayLabels = ["一", "三", "五"]; // 周一/三/五 标签

  const hoverPoint = hover != null ? data[hover] : null;

  // 总计统计
  const totals = useMemo(() => {
    const totalTokens = data.reduce((sum, d) => sum + d.tokens, 0);
    const totalCost = data.reduce((sum, d) => sum + d.cost, 0);
    const activeDays = data.filter((d) => d.tokens > 0).length;
    return { totalTokens, totalCost, activeDays };
  }, [data]);

  return (
    <div className="space-y-3">
      {/* 统计摘要 */}
      <div className="flex flex-wrap items-center gap-4 text-xs text-ink-muted">
        <span>
          活跃天数{" "}
          <span className="num font-semibold text-ink">
            {totals.activeDays}
          </span>
        </span>
        <span>
          总计{" "}
          <span className="num font-semibold text-ink">
            {formatNumber(totals.totalTokens, { notation: "compact" })}
          </span>{" "}
          tokens
        </span>
        {totals.totalCost > 0 && (
          <span>
            预计消耗{" "}
            <span className="num font-semibold text-ink">
              ${totals.totalCost.toFixed(2)}
            </span>
          </span>
        )}
      </div>

      <div className="relative overflow-x-auto">
        <svg
          viewBox={`0 0 ${svgWidth} ${svgHeight}`}
          className="w-full"
          style={{ minWidth: `${svgWidth}px` }}
          role="img"
          aria-label="使用热力图"
        >
          {/* 月份标签 */}
          {monthLabels.map((label, i) => (
            <text
              key={i}
              x={label.x}
              y={12}
              className="fill-ink-muted text-[9px]"
            >
              {label.text}
            </text>
          ))}

          {/* 星期标签 */}
          {weekdayLabels.map((label, i) => (
            <text
              key={i}
              x={0}
              y={18 + (i * 2 + 1) * colWidth + cellSize / 2 + 3}
              className="fill-ink-muted text-[9px]"
            >
              {label}
            </text>
          ))}

          {/* 热力图格子 */}
          {columns.map((col, colIdx) =>
            col.map((cell, rowIdx) => {
              const level = cell.point ? getLevel(cell.point.tokens) : 0;
              const x = labelWidth + colIdx * colWidth;
              const y = 18 + rowIdx * colWidth;
              const flatIdx = data.findIndex(
                (d) => d.date === cell.date,
              );
              return (
                <rect
                  key={`${colIdx}-${rowIdx}`}
                  x={x}
                  y={y}
                  width={cellSize}
                  height={cellSize}
                  rx={2}
                  className={LEVEL_COLORS[level]}
                  strokeWidth={0.5}
                  onMouseEnter={() =>
                    flatIdx >= 0 && setHover(flatIdx)
                  }
                  onMouseLeave={() => setHover(null)}
                >
                  <title>
                    {cell.date}:{" "}
                    {cell.point
                      ? `${formatNumber(cell.point.tokens)} tokens · $${cell.point.cost.toFixed(4)} · ${cell.point.count} 请求`
                      : "无数据"}
                  </title>
                </rect>
              );
            }),
          )}
        </svg>

        {/* hover tooltip */}
        {hoverPoint && (
          <div className="pointer-events-none absolute left-1/2 top-0 z-10 -translate-x-1/2 rounded-lg border border-border bg-surface px-3 py-2 text-xs shadow-md">
            <div className="font-medium text-ink">{hoverPoint.date}</div>
            <div className="mt-0.5 text-ink-muted">
              {formatNumber(hoverPoint.tokens)} tokens ·{" "}
              {hoverPoint.count} 请求
            </div>
            {hoverPoint.cost > 0 && (
              <div className="text-ink-muted">
                ${hoverPoint.cost.toFixed(4)}
              </div>
            )}
          </div>
        )}
      </div>

      {/* 图例 */}
      <div className="flex items-center justify-end gap-1.5 text-[10px] text-ink-muted">
        <span>少</span>
        {LEVEL_COLORS.map((color, i) => (
          <span
            key={i}
            className={`inline-block h-2.5 w-2.5 rounded-sm ${color}`}
          />
        ))}
        <span>多</span>
      </div>
    </div>
  );
}

function formatDate(d: Date): string {
  const y = d.getFullYear();
  const m = (d.getMonth() + 1).toString().padStart(2, "0");
  const day = d.getDate().toString().padStart(2, "0");
  return `${y}-${m}-${day}`;
}
