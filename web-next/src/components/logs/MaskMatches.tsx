import { Pill } from "@/components/ui/pill";
import { cn } from "@/lib/utils";
import type { MaskMatchSummary } from "@/lib/types";

/**
 * MaskMatches —— 脱敏命中明细展示（文档 07 §3.3）
 *
 * 实施边界修订(文档 07): 日志命中明细只展示规则标签 + 占位符安全摘要,
 * 不展示命中原文 original。查看日志原文不是已批准能力, 需另行安全决策;
 * 不能靠管理员鉴权或 CSS 模糊代替, 前端直接不渲染原文。
 *
 * 复用形状与后端 RequestState.mask_matches / ErrorLog.mask_matches 一致:
 *   { label, placeholder }
 *
 *  - 每条一行：规则标签 Pill（PHONE/EMAIL/SECRET/TERM…）+ 占位符（等宽）
 *  - 空数组 / undefined → 返回 null，不渲染整个区域（与「已脱敏」标记互证：
 *    标记存在但无明细 = 旧版本进程 / 开关后关特例）
 *  - 不新增任何 API 调用：命中明细随 RequestState 状态流 / 持久化 ErrorLog 下发
 */
export function MaskMatches({
  matches,
  title = "脱敏命中",
  caption = "仅本请求命中明细",
  className,
}: {
  matches?: MaskMatchSummary[] | null;
  title?: string;
  /** 右上角小字标注；传空串隐藏 */
  caption?: string;
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
        {caption && <span className="ml-auto">{caption}</span>}
      </div>
      <ul className="divide-y divide-border/60">
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
    <li className="flex items-center gap-2 px-3 py-1.5 text-xs">
      <Pill tone={tone} dot={false} className="shrink-0">
        {match.label}
      </Pill>
      <code className="mono shrink-0 text-ink-muted">{match.placeholder}</code>
    </li>
  );
}
