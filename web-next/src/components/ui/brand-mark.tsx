import { cn } from "@/lib/utils";

type BrandMarkSize = "sm" | "md" | "lg";

const SIZE: Record<BrandMarkSize, { box: string; icon: string; title: string; caption: string }> = {
  sm: { box: "h-8 w-8 rounded-[10px]", icon: "h-5 w-5", title: "text-[15px]", caption: "text-[11px]" },
  md: { box: "h-9 w-9 rounded-[11px]", icon: "h-[22px] w-[22px]", title: "text-base", caption: "text-xs" },
  lg: { box: "h-14 w-14 rounded-[16px]", icon: "h-8 w-8", title: "text-xl", caption: "text-[13px]" },
};

/**
 * 品牌标识 —— 使用 public/logo.svg 的星芒+面纱图形，
 * 避免各处手写渐变「N」与真实 Logo 脱节。
 */
export function BrandMark({
  size = "md",
  withName = false,
  stacked = false,
  caption,
  heading = false,
  className,
}: {
  size?: BrandMarkSize;
  withName?: boolean;
  stacked?: boolean;
  caption?: string;
  heading?: boolean;
  className?: string;
}) {
  const s = SIZE[size];
  const NameTag = heading ? "h1" : "p";
  return (
    <div
      className={cn(
        "flex items-center gap-2.5",
        stacked && "flex-col gap-3",
        className,
      )}
    >
      <div
        aria-hidden
        className={cn(
          "brand-tile flex shrink-0 items-center justify-center ring-1 ring-white/60",
          s.box,
        )}
      >
        <img src="/logo.svg" alt="" className={s.icon} />
      </div>
      {withName && (
        <div className={cn(stacked && "text-center")}>
          <NameTag className={cn("font-semibold tracking-tight text-ink", s.title)}>
            NovaVeil
          </NameTag>
          {caption && (
            <p className={cn("mt-0.5 text-ink-muted", s.caption)}>{caption}</p>
          )}
        </div>
      )}
    </div>
  );
}
