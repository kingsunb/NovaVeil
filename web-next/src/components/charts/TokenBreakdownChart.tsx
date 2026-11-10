import { formatNumber } from "@/lib/utils";
import { TOKEN_IN_COLOR, TOKEN_OUT_COLOR } from "./token-colors";

/**
 * TokenBreakdownChart —— Token 构成环形图。
 * 展示 input / output 两类 token 的占比, 用纯 SVG 环形图实现。
 */
export function TokenBreakdownChart({
  input,
  output,
}: {
  input: number;
  output: number;
}) {
  const normalizeTokenCount = (value: number) =>
    Number.isFinite(value) ? Math.max(0, value) : 0;
  const segments = [
    { label: "输入", value: normalizeTokenCount(input), color: TOKEN_IN_COLOR },
    { label: "输出", value: normalizeTokenCount(output), color: TOKEN_OUT_COLOR },
  ];

  const total = segments.reduce((sum, s) => sum + s.value, 0);
  const hasData = total > 0;

  // 环形图参数
  const size = 160;
  const center = size / 2;
  const radius = 60;
  const strokeWidth = 20;

  // 计算各段弧路径
  const arcs = (() => {
    if (!hasData) return [];
    let cumulative = 0;
    return segments
      .filter((s) => s.value > 0)
      .map((s) => {
        const startAngle = (cumulative / total) * 2 * Math.PI - Math.PI / 2;
        cumulative += s.value;
        const endAngle = (cumulative / total) * 2 * Math.PI - Math.PI / 2;
        return {
          ...s,
          startAngle,
          endAngle,
          pct: (s.value / total) * 100,
        };
      });
  })();

  const arcPath = (startAngle: number, endAngle: number) => {
    const x1 = center + radius * Math.cos(startAngle);
    const y1 = center + radius * Math.sin(startAngle);
    const x2 = center + radius * Math.cos(endAngle);
    const y2 = center + radius * Math.sin(endAngle);
    const angleSpan = endAngle - startAngle;
    if (angleSpan >= 2 * Math.PI) {
      const midAngle = startAngle + Math.PI;
      const xm = center + radius * Math.cos(midAngle);
      const ym = center + radius * Math.sin(midAngle);
      return `M ${x1} ${y1} A ${radius} ${radius} 0 1 1 ${xm} ${ym} A ${radius} ${radius} 0 1 1 ${x1} ${y1}`;
    }
    const largeArc = angleSpan > Math.PI ? 1 : 0;
    return `M ${x1} ${y1} A ${radius} ${radius} 0 ${largeArc} 1 ${x2} ${y2}`;
  };

  return (
    <div className="flex flex-col items-center gap-6 sm:flex-row">
      {/* 环形图 */}
      <div className="relative shrink-0">
        <svg width={size} height={size} role="img" aria-label="Token 构成图">
          {/* 背景圆环 */}
          <circle
            cx={center}
            cy={center}
            r={radius}
            fill="none"
            strokeWidth={strokeWidth}
            className="stroke-surface-subtle"
          />
          {/* 各段弧 */}
          {arcs.map((arc, i) => (
            <path
              key={i}
              d={arcPath(arc.startAngle, arc.endAngle)}
              fill="none"
              stroke={arc.color}
              strokeWidth={strokeWidth}
              strokeLinecap="round"
            />
          ))}
        </svg>
        {/* 中心总量 */}
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="num text-lg font-semibold text-ink">
            {hasData
              ? formatNumber(total, { notation: "compact" })
              : "0"}
          </span>
          <span className="text-[10px] text-ink-muted">总 tokens</span>
        </div>
      </div>

      {/* 图例 */}
      <div className="flex-1 space-y-2">
        {segments.map((s) => (
          <div key={s.label} className="flex items-center justify-between gap-3 text-xs">
            <div className="flex items-center gap-2">
              <span
                className="inline-block h-2.5 w-2.5 rounded-full"
                style={{ backgroundColor: s.color }}
              />
              <span className="text-ink">{s.label}</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="num text-ink-muted">
                {formatNumber(s.value, { notation: "compact" })}
              </span>
              <span className="num w-10 text-right text-ink-subtle">
                {hasData ? `${((s.value / total) * 100).toFixed(1)}%` : "—"}
              </span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
