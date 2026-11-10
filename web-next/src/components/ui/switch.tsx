import * as React from "react";
import * as SwitchPrimitives from "@radix-ui/react-switch";
import { cn } from "@/lib/utils";

/**
 * 开关 —— macOS 风格
 *  - Apple 绿色（checked）+ 浅灰（unchecked）
 *  - 按下时 thumb 微缩放
 */
export const Switch = React.forwardRef<
  React.ElementRef<typeof SwitchPrimitives.Root>,
  React.ComponentPropsWithoutRef<typeof SwitchPrimitives.Root>
>(({ className, ...props }, ref) => (
  <SwitchPrimitives.Root
    className={cn(
      "peer inline-flex h-[26px] w-[44px] shrink-0 cursor-pointer items-center rounded-full border border-border transition-all duration-200 ease-[cubic-bezier(0.25,0,0,1)]",
      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
      "disabled:cursor-not-allowed disabled:opacity-50",
      "data-[state=checked]:border-transparent data-[state=checked]:bg-[#30D158] data-[state=unchecked]:bg-ink/20",
      className,
    )}
    {...props}
    ref={ref}
  >
    <SwitchPrimitives.Thumb
      className={cn(
        "pointer-events-none block h-[22px] w-[22px] rounded-full bg-white shadow-[0_2px_4px_rgba(0,0,0,0.15)] transition-transform duration-200 ease-[cubic-bezier(0.25,0,0,1)]",
        "data-[state=checked]:translate-x-[18px] data-[state=unchecked]:translate-x-[2px]",
      )}
    />
  </SwitchPrimitives.Root>
));
Switch.displayName = SwitchPrimitives.Root.displayName;
