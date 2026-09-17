import { Pill } from "@/components/ui/pill";
import { cn } from "@/lib/utils";
import type { MaskMatchSummary } from "@/lib/types";

/**
 * MaskMatches —— 脱敏命中明细展示（文档 07 §3.3）
 *
 * 决策变更(文档 07): 已批准展示命中原文 original, 每条命中展示
 * 规则标签 + 命中原文 + 占位符, 供管理员在日志详情定位被脱敏的原文。
 *
 * 复用形状与后端 RequestState.mask_matches / ErrorLog.mask_matches 一致:
 *   { label, original, placeholder }
 *
 *  - 每条：规则标签 Pill（PHONE/EMAIL/SECRET/TERM…）+ 占位符（等宽）+ 原文
 *  - 空数组 / undefined → 返回 null，不渲染整个区域（与「已脱敏」标记互证：
 *    标记存在但无明细 = 旧版本进程 / 开关后关特例）
 *  - 不新增任何 API 调用：命中明细随 RequestState 状态流 / 持久化 ErrorLog 下发
 */
export function MaskMatches({
  matches,
  title = "脱敏命中",
  caption = "仅本请求命中明细",
  truncated = false,
  className,
}: {
  matches?: MaskMatchSummary[] | null;
  title?: string;
  /** 右上角小字标注；传空串隐藏 */
  caption?: string;
  /** true=命中明细因条数/字节上限被裁剪, 仅展示部分命中(文档 07 §3.1 第 5 点、design §2.1.3) */
  truncated?: boolean;
  className?: string;
}) {
  if (!matches || matches.length === 0) return null;

  return (
    <div
      className={cn(
        "rounded-md border border-border/60 bg-surface-subtle/20",
        className,
      )}
      data-testid="mask-matches"
    >
      <div className="flex items-center gap-2 border-b border-border/60 px-3 py-1.5 text-[11px] text-ink-muted">
        <span className="font-medium text-ink">{title}</span>
        <Pill tone="neutral" dot={false}>
          {matches.length}
        </Pill>
        {truncated && (
          <Pill tone="warning" dot={false}>
            命中明细已截断，仅展示部分命中
          </Pill>
        )}
        {caption && <span className="ml-auto">{caption}</span>}
      </div>
      <ul className="max-h-72 divide-y divide-border/60 overflow-auto">
        {matches.map((m, i) => (
          <MaskMatchRow
            key={`${m.label}-${m.placeholder}-${i}`}
            match={m}
          />
        ))}
      </ul>
    </div>
  );
}

/** 规则标签 → Pill 语义色；未知标签回退 neutral。 */
const LABEL_TONE: Record<
  string,
  "neutral" | "info" | "success" | "warning" | "danger"
> = {
  PHONE: "info",
  EMAIL: "info",
  SECRET: "danger",
  TERM: "warning",
};

function MaskMatchRow({ match }: { match: MaskMatchSummary }) {
  const tone = LABEL_TONE[match.label] ?? "neutral";

  return (
    <li className="flex flex-col gap-1 px-3 py-2 text-xs">
      <div className="flex items-center gap-2">
        <Pill tone={tone} dot={false} className="shrink-0">
          {match.label}
        </Pill>
        <code className="mono shrink-0 text-ink-muted">{match.placeholder}</code>
      </div>
      {match.original && (
        <div className="flex items-start gap-1.5 pl-1">
          <span className="shrink-0 text-[10px] uppercase tracking-wide text-ink-subtle">
            原文
          </span>
          <code
            className="mono min-w-0 flex-1 break-all text-ink"
            title={match.original}
          >
            {match.original}
          </code>
        </div>
      )}
    </li>
  );
}
