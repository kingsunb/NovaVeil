import { useEffect, useState } from "react";
import { formatNumber } from "@/lib/utils";
import type { TokenTrendPoint, TokenTrendRange } from "@/lib/types";
import { TOKEN_IN_COLOR, TOKEN_OUT_COLOR } from "./token-colors";

type Range = TokenTrendRange;

/**
 * TokenTrendChart —— 双折线趋势图（入蓝 / 出绿），hover 显示精确值。
 * 数据来自 /api/v1/update/token-trends 的真实分桶时序（24h/7d/30d），
 * 点数 24/28/30 由后端保证；全零/空数据渲染「暂无数据」占位。
 */
export function TokenTrendChart({
  range,
  data,
}: {
  range: Range;
  data: TokenTrendPoint[];
}) {
  const [hover, setHover] = useState<number | null>(null);

  // 数据或档位切换后旧 hover 下标不再对应新序列,重置避免残留错位 tooltip。
  useEffect(() => {
    setHover(null);
  }, [data, range]);

  const w = 720;
  const h = 200;
  const padL = 36;
  const padR = 12;
  const padT = 12;
  const padB = 22;
  const innerW = w - padL - padR;
  const innerH = h - padT - padB;

  const hasData = data.length > 0 && data.some((d) => d.in > 0 || d.out > 0);
  const points = hasData ? data : [];
  const max =
    Math.max(...points.map((d) => Math.max(d.in, d.out)), 1);
  const niceMax = niceCeil(max);
  const yTicks = 4;

  const x = (i: number) =>
    padL + (i / Math.max(1, points.length - 1)) * innerW;
  const y = (v: number) => padT + innerH - (v / niceMax) * innerH;

  const linePath = (key: "in" | "out") =>
    points.map((d, i) => `${i ? "L" : "M"}${x(i)},${y(d[key])}`).join(" ");

  const hoverPoint = hover != null ? points[hover] : null;

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${w} ${h}`}
        className="h-48 w-full"
        role="img"
        aria-label="Token 用量趋势图"
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          if (points.length === 0) return;
          const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
          // 换算到 viewBox 坐标再限定绘图区 [padL, w-padR]:
          // padding 区(y 轴标签/右侧留白)不响应 hover,避免错选首末点。
          const vx = ((e.clientX - rect.left) / rect.width) * w;
          const rel = (vx - padL) / innerW;
          if (rel < 0 || rel > 1) {
            setHover(null);
            return;
          }
          setHover(Math.round(rel * (points.length - 1)));
        }}
      >
        {/* 网格 + Y 轴 */}
        {Array.from({ length: yTicks + 1 }).map((_, i) => {
          const v = (niceMax / yTicks) * i;
          const yy = y(v);
          return (
            <g key={i}>
              <line
                x1={padL}
                x2={w - padR}
                y1={yy}
                y2={yy}
                stroke="currentColor"
                strokeOpacity={0.08}
              />
              <text
                x={padL - 6}
                y={yy + 3}
                textAnchor="end"
                className="fill-ink-subtle text-[10px] tabular-nums"
              >
                {formatNumber(v, { notation: "compact" })}
              </text>
            </g>
          );
        })}

        {/* X 轴标签（首/中/末） */}
        {points.length > 0 &&
          [0, Math.floor(points.length / 2), points.length - 1].map((i) => (
            <text
              key={i}
              x={x(i)}
              y={h - 6}
              textAnchor="middle"
              className="fill-ink-subtle text-[10px]"
            >
              {fmtTick(points[i].t, range)}
            </text>
          ))}

        {/* 数据线 */}
        {points.length > 1 && (
          <>
            <path
              d={linePath("in")}
              fill="none"
              stroke={TOKEN_IN_COLOR}
              strokeWidth={1.5}
              strokeLinecap="round"
              strokeLinejoin="round"
            />
            <path
              d={linePath("out")}
              fill="none"
              stroke={TOKEN_OUT_COLOR}
              strokeWidth={1.5}
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </>
        )}

        {/* hover 指示 */}
        {hover != null && hoverPoint && (
          <g>
            <line
              x1={x(hover)}
              x2={x(hover)}
              y1={padT}
              y2={padT + innerH}
              stroke="currentColor"
              strokeOpacity={0.15}
            />
            <circle
              cx={x(hover)}
              cy={y(points[hover].in)}
              r="3"
              fill={TOKEN_IN_COLOR}
            />
            <circle
              cx={x(hover)}
              cy={y(points[hover].out)}
              r="3"
              fill={TOKEN_OUT_COLOR}
            />
          </g>
        )}
      </svg>

      {!hasData && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
          <span className="rounded-full border border-border bg-card/90 px-3 py-1 text-[11px] text-ink-muted shadow-sm">
            暂无用量数据
          </span>
        </div>
      )}

      {hoverPoint && (
        <div
          data-testid="token-trend-tooltip"
          className="pointer-events-none absolute -top-1 left-0 rounded-md border border-border bg-card/95 px-2.5 py-1.5 text-[11px] shadow-overlay backdrop-blur"
          style={{
            // 居中于数据点(约 130px 宽),首末点处夹回容器内不溢出。
            transform: `translateX(${Math.min(Math.max(x(hover ?? 0) - 60, 0), w - 130)}px)`,
          }}
        >
          <div className="text-ink-muted">{fmtFull(hoverPoint.t, range)}</div>
          <div className="mt-0.5 flex items-center gap-3">
            <span className="flex items-center gap-1">
              <span
                className="h-1.5 w-1.5 rounded-full"
                style={{ backgroundColor: TOKEN_IN_COLOR }}
              />
              <span className="text-ink">入</span>
              <span className="num text-ink">
                {formatNumber(hoverPoint.in)}
              </span>
            </span>
            <span className="flex items-center gap-1">
              <span
                className="h-1.5 w-1.5 rounded-full"
                style={{ backgroundColor: TOKEN_OUT_COLOR }}
              />
              <span className="text-ink">出</span>
              <span className="num text-ink">
                {formatNumber(hoverPoint.out)}
              </span>
            </span>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- helpers ----------

function niceCeil(v: number) {
  const exp = Math.floor(Math.log10(v));
  const f = v / 10 ** exp;
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nice * 10 ** exp;
}

function fmtTick(t: number, range: Range) {
  const d = new Date(t);
  if (range === "24h") {
    return `${String(d.getHours()).padStart(2, "0")}:00`;
  }
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

function fmtFull(t: number, range: Range) {
  const d = new Date(t);
  if (range === "24h") {
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}:00`;
  }
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}
