import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/**
 * 按钮 —— 暖光渐变磨砂风
 *  - 胶囊圆角 + 微动效（按下缩放）
 *  - primary: 四色渐变 pill + 深紫文字
 *  - secondary: 玻璃面 + 半透明
 *  - ghost: 透明底 + hover 微亮
 */
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-full text-[13px] font-medium tracking-tight transition-all duration-150 ease-[cubic-bezier(0.25,0,0,1)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 active:scale-[0.97] disabled:pointer-events-none disabled:opacity-40",
  {
    variants: {
      variant: {
        primary:
          "gradient-cta shadow-apple-sm hover:shadow-apple-md",
        secondary:
          "glass-panel glass-inset-highlight text-secondary-foreground hover:brightness-[1.02]",
        ghost:
          "text-ink-muted hover:bg-ink/[0.04] hover:text-ink",
        outline:
          "border border-border/60 bg-card/50 text-ink backdrop-blur-sm hover:bg-card/70",
        destructive:
          "bg-destructive text-destructive-foreground shadow-apple-sm hover:shadow-apple-md hover:brightness-110",
        "danger-outline":
          "border border-destructive/30 text-destructive hover:bg-destructive/8",
        link: "text-primary-text underline-offset-4 hover:underline",
      },
      size: {
        sm: "h-7 px-3 text-xs",
        md: "h-8 px-4",
        lg: "h-10 px-5 text-sm",
        icon: "h-8 w-8",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "md",
    },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
  loading?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  (
    {
      className,
      variant,
      size,
      asChild = false,
      loading,
      children,
      disabled,
      type = "button",
      ...props
    },
    ref,
  ) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        type={asChild ? undefined : type}
        disabled={disabled || loading}
        {...props}
      >
        {loading ? (
          <span className="flex items-center gap-1.5">
            <span className="h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent" />
            {children}
          </span>
        ) : (
          children
        )}
      </Comp>
    );
  },
);
Button.displayName = "Button";

export { buttonVariants };
