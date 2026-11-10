import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;
export const DialogPortal = DialogPrimitive.Portal;

const DialogOverlay = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Overlay
    ref={ref}
    className={cn(
      "fixed inset-0 z-50 bg-black/20 backdrop-blur-[6px]",
      "data-[state=open]:animate-in data-[state=closed]:animate-out duration-200",
      "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
      className,
    )}
    {...props}
  />
));
DialogOverlay.displayName = "DialogOverlay";

export const DialogContent = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & {
    /** "dialog" 居中（默认），"sheet" 右侧滑出，"fullscreen" 全屏覆盖，"wide" 移动端全宽/桌面 80% 靠右 */
    variant?: "dialog" | "sheet" | "fullscreen" | "wide";
    size?: "sm" | "md" | "lg";
  }
>(({ className, children, variant = "dialog", size = "md", ...props }, ref) => (
  <DialogPortal>
    <DialogOverlay />
    <DialogPrimitive.Content
      ref={ref}
      className={cn(
        // fullscreen / wide 覆盖整个视口, 背后是仍在 SSE 驱动下频繁重渲染的 LiveTable;
        // 若沿用 glass-overlay 的 backdrop-filter: blur(40px), 每条 SSE 消息都
        // 触发一次全屏 40px 模糊重合成, 导致详情页卡死。fullscreen / wide 改用不透明
        // 背景, 消除 backdrop-filter 这一 GPU 瓶颈。
        variant === "fullscreen" || variant === "wide"
          ? "fixed z-50 bg-card"
          : "glass-overlay fixed z-50",
        "data-[state=open]:animate-in data-[state=closed]:animate-out duration-200",
        variant === "dialog" &&
          "left-1/2 top-1/2 flex max-h-[min(90dvh,calc(100dvh-2rem))] w-[calc(100vw-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-card data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
        variant === "sheet" &&
          "right-0 top-0 flex h-full w-full max-w-xl flex-col rounded-l-card data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right sm:max-w-xl",
        variant === "fullscreen" &&
          "inset-0 flex h-full w-full flex-col rounded-none data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        // wide: 移动端全宽, 桌面靠右八成。
        variant === "wide" &&
          "right-0 top-0 flex h-full w-full flex-col rounded-l-card data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right sm:w-4/5",
        size === "sm" && variant === "dialog" && "max-w-sm",
        size === "lg" && variant === "dialog" && "max-w-2xl",
        className,
      )}
      {...props}
    >
      {children}
      <DialogPrimitive.Close
        aria-label="关闭"
        className="absolute right-4 top-4 flex h-7 w-7 items-center justify-center rounded-full text-ink-subtle transition-all hover:bg-ink/[0.06] hover:text-ink active:scale-95 focus:outline-none focus:ring-2 focus:ring-ring"
      >
        <X className="h-3.5 w-3.5" aria-hidden />
      </DialogPrimitive.Close>
    </DialogPrimitive.Content>
  </DialogPortal>
));
DialogContent.displayName = "DialogContent";

export const DialogHeader = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn(
      // pr-12：给右上角绝对定位的关闭钮留位，长标题不压到 X
      "flex flex-col gap-1 border-b border-border/60 px-5 py-4 pr-12",
      className,
    )}
    {...props}
  />
);
DialogHeader.displayName = "DialogHeader";

export const DialogTitle = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title
    ref={ref}
    className={cn(
      "text-sm font-semibold leading-5 tracking-tight text-ink",
      className,
    )}
    {...props}
  />
));
DialogTitle.displayName = "DialogTitle";

export const DialogDescription = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description
    ref={ref}
    className={cn("text-xs text-ink-muted", className)}
    {...props}
  />
));
DialogDescription.displayName = "DialogDescription";

export const DialogBody = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex-1 overflow-y-auto p-5", className)} {...props} />
);
DialogBody.displayName = "DialogBody";

export const DialogFooter = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn(
      "flex shrink-0 items-center justify-end gap-2 border-t border-border/60 px-5 py-4",
      className,
    )}
    {...props}
  />
);
DialogFooter.displayName = "DialogFooter";
