import { cn } from "@/lib/utils";

export interface SegmentOption<T extends string = string> {
  value: T;
  label: string;
}

/**
 * 分段控件 —— 状态筛选 / 时间档位 / 主题切换共用
 * 保持原生 button + aria-pressed，不改各页既有测试选择器。
 */
export function SegmentedControl<T extends string>({
  value,
  onChange,
  options,
  size = "sm",
  className,
  "aria-label": ariaLabel,
}: {
  value: T;
  onChange: (value: T) => void;
  options: readonly SegmentOption<T>[];
  size?: "sm" | "md";
  className?: string;
  "aria-label"?: string;
}) {
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={cn(
        "inline-flex items-center rounded-control border border-border/60 bg-card/70 p-0.5 backdrop-blur-sm",
        className,
      )}
    >
      {options.map((opt) => {
        const selected = value === opt.value;
        return (
          <button
            key={opt.value}
            type="button"
            aria-pressed={selected}
            onClick={() => onChange(opt.value)}
            className={cn(
              "rounded-[6px] font-medium tracking-tight transition-all duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
              size === "sm" ? "px-2.5 py-1 text-xs" : "px-3 py-1.5 text-[13px]",
              selected
                ? "bg-primary/[0.12] text-primary-text shadow-apple-sm"
                : "text-ink-muted hover:bg-ink/[0.04] hover:text-ink",
            )}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
