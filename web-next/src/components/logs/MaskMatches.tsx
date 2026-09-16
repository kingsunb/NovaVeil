import { useState } from "react";

import { Pill } from "@/components/ui/pill";
import { cn } from "@/lib/utils";
import type { MaskTestMatch } from "@/lib/types";

/**
 * MaskMatches —— 脱敏命中明细展示（文档 07 §3.3）
 *
 * 复用 shape 与后端 handlers.maskTestMatch / RequestState.mask_matches 一致：
 *   { label, original, placeholder }
 *
 *  - 每条一行：规则标签 Pill（PHONE/EMAIL/SECRET/TERM…）+ 占位符（等宽）+ 命中原文
 *  - 原文属敏感内容：默认折叠（仅给「显示原文」弱化按钮），点击展开后以等宽小字弱化展示
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
  matches?: MaskTestMatch[] | null;
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

function MaskMatchRow({ match }: { match: MaskTestMatch }) {
  const [expanded, setExpanded] = useState(false);
  const tone = LABEL_TONE[match.label] ?? "neutral";

  return (
    <li className="flex items-start gap-2 px-3 py-1.5 text-xs">
      <Pill tone={tone} dot={false} className="shrink-0">
        {match.label}
      </Pill>
      <code className="mono shrink-0 text-ink-muted">{match.placeholder}</code>
      <div className="ml-auto flex min-w-0 items-start justify-end gap-1">
        {expanded ? (
          <>
            <code
              className="mono max-w-full break-all text-right text-[11px] leading-relaxed text-ink-subtle"
              // 命中原文为敏感内容：等宽小字 + 弱化色，仅管理员面可见
            >
              {match.original}
            </code>
            <button
              type="button"
              onClick={() => setExpanded(false)}
              className="shrink-0 rounded-control px-1.5 py-0.5 text-[11px] text-ink-subtle transition-colors hover:bg-surface-subtle/60 hover:text-ink"
            >
              收起
            </button>
          </>
        ) : (
          <button
            type="button"
            onClick={() => setExpanded(true)}
            title="显示命中原文（敏感内容）"
            className="shrink-0 rounded-control px-1.5 py-0.5 text-[11px] text-ink-subtle transition-colors hover:bg-surface-subtle/60 hover:text-ink"
          >
            显示原文
          </button>
        )}
      </div>
    </li>
  );
}
