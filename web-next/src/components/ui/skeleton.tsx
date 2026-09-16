import { cn } from "@/lib/utils";

/**
 * 骨架屏 —— macOS 风格
 *  - 与最终内容同形状的占位（避免布局跳变）
 *  - Apple shimmer 动画
 */

/** 基础骨架条 */
export function Skeleton({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      aria-hidden
      className={cn(
        "animate-pulse rounded-md bg-ink/[0.06]",
        className,
      )}
      {...props}
    />
  );
}

/** 页面级骨架（Dashboard 等） */
export function PageSkeleton() {
  return (
    <div className="space-y-6" role="status" aria-label="加载中">
      {/* KPI 行 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {[...Array(4)].map((_, i) => (
          <div key={i} className="glass-panel rounded-card p-5">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="mt-3 h-8 w-24" />
            <Skeleton className="mt-2 h-2.5 w-20" />
          </div>
        ))}
      </div>
      {/* 图表区 */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <div className="glass-panel rounded-card p-5 xl:col-span-2">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="mt-2 h-3 w-48" />
          <Skeleton className="mt-6 h-48 w-full" />
        </div>
        <div className="space-y-4">
          <div className="glass-panel rounded-card p-5">
            <Skeleton className="h-4 w-24" />
            {[...Array(4)].map((_, i) => (
              <div key={i} className="mt-4 flex items-center justify-between">
                <Skeleton className="h-3 w-20" />
                <Skeleton className="h-3 w-12" />
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** 登录页骨架：复用基础骨架条，不挂载可交互的表单控件。 */
export function LoginSkeleton() {
  return (
    <div className="bg-gradient-subtle flex min-h-full items-center justify-center bg-background px-4">
      <div
        className="glass-panel glass-inset-highlight w-full max-w-[380px] rounded-card p-8 shadow-apple-lg"
        role="status"
        aria-label="加载中"
      >
        <div className="mb-8 flex flex-col items-center gap-3">
          <Skeleton className="h-12 w-12 rounded-card" />
          <Skeleton className="h-5 w-28" />
          <Skeleton className="h-3 w-36" />
        </div>
        <div className="space-y-4">
          {[0, 1].map((i) => (
            <div key={i}>
              <Skeleton className="mb-1.5 h-3 w-12" />
              <Skeleton className="h-10 w-full rounded-control" />
            </div>
          ))}
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-10 w-full rounded-control" />
        </div>
        <Skeleton className="mx-auto mt-6 h-3 w-full" />
      </div>
    </div>
  );
}

/** 表格骨架（Channels/Groups/Keys） */
export function TableSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div className="glass-panel rounded-card" role="status" aria-label="加载中">
      {/* 表头 */}
      <div className="flex items-center gap-4 border-b border-border/40 px-5 py-3">
        {[...Array(5)].map((_, i) => (
          <Skeleton key={i} className="h-3 flex-1" />
        ))}
      </div>
      {/* 行 */}
      {[...Array(rows)].map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-4 border-b border-border/20 px-5 py-3.5 last:border-b-0"
        >
          {[...Array(5)].map((_, j) => (
            <Skeleton key={j} className="h-3.5 flex-1" />
          ))}
        </div>
      ))}
    </div>
  );
}

/** 卡片网格骨架（Groups） */
export function CardGridSkeleton({ cards = 6 }: { cards?: number }) {
  return (
    <div
      className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3"
      role="status"
      aria-label="加载中"
    >
      {[...Array(cards)].map((_, i) => (
        <div key={i} className="glass-panel rounded-card p-5">
          <div className="flex items-center justify-between">
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-5 w-12 rounded-full" />
          </div>
          {[...Array(3)].map((_, j) => (
            <div key={j} className="mt-3 flex items-center justify-between">
              <Skeleton className="h-3 w-20" />
              <Skeleton className="h-3 w-10" />
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

/** 设置页骨架 */
export function SettingsSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-[180px_1fr]" role="status" aria-label="加载中">
      <div className="space-y-1">
        {[...Array(8)].map((_, i) => (
          <Skeleton key={i} className="h-8 w-full rounded-control" />
        ))}
      </div>
      <div className="glass-panel rounded-card p-6">
        <Skeleton className="h-4 w-20" />
        <Skeleton className="mt-2 h-3 w-32" />
        <div className="mt-6 space-y-4">
          {[...Array(4)].map((_, i) => (
            <div key={i}>
              <Skeleton className="h-3 w-24" />
              <Skeleton className="mt-1.5 h-8 w-full rounded-control" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

/** 日志页骨架（含虚拟化容器） */
export function LogsSkeleton() {
  return (
    <div className="space-y-4" role="status" aria-label="加载中">
      {/* 计数条 */}
      <div className="glass-panel flex items-center gap-6 rounded-control px-5 py-3">
        {[...Array(4)].map((_, i) => (
          <div key={i} className="flex items-center gap-2">
            <Skeleton className="h-2 w-2 rounded-full" />
            <Skeleton className="h-3 w-10" />
            <Skeleton className="h-3 w-8" />
          </div>
        ))}
      </div>
      {/* Tab */}
      <Skeleton className="h-8 w-48 rounded-full" />
      {/* 表格 */}
      <TableSkeleton rows={8} />
    </div>
  );
}
