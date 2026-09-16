import { formatNumber } from "@/lib/utils";

/**
 * TokenBreakdownChart —— Token 构成环形图。
 * 展示 input / output / reasoning / cached 四类 token 的占比,
 * 参考 TokenArena 的详细指标功能, 用纯 SVG 环形图实现。
 */
export function TokenBreakdownChart({
  input,
  output,
  reasoning,
  cached,
}: {
  input: number;
  output: number;
  reasoning: number;
  cached: number;
}) {
  const segments = [
    { label: "输入", value: input, color: "#60A5FA" },
    { label: "输出", value: output, color: "#34D399" },
    { label: "推理", value: reasoning, color: "#F59E0B" },
    { label: "缓存命中", value: cached, color: "#A78BFA" },
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
    const largeArc = endAngle - startAngle > Math.PI ? 1 : 0;
    return `M ${x1} ${y1} A ${radius} ${radius} 0 ${largeArc} 1 ${x2} ${y2}`;
  };

  return (
    <div className="flex items-center gap-6">
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
