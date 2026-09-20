import { useEffect, useState } from "react";
import { Check } from "lucide-react";

import { Button } from "./button";

/**
 * 两段式确认按钮：首次点击进入「再次点击确认」待命态，2.2s 内未再次点击自动复位；
 * 这样既能阻止误触，又避免每次操作都要二次确认对话框，适合危险但频繁的删除动作。
 */
export function ConfirmButton({
  onConfirm,
  label = "删除",
  loadingLabel = "删除中…",
  loading,
  disabled,
  tone = "primary",
}: {
  onConfirm: () => void;
  label?: string;
  /** loading 时显示的替换文案，让「清空归档」等非删除操作也能复用本组件。 */
  loadingLabel?: string;
  loading?: boolean;
  disabled?: boolean;
  tone?: "primary" | "destructive";
}) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const timer = setTimeout(() => setArmed(false), 2200);
    return () => clearTimeout(timer);
  }, [armed]);
  const isDisabled = loading || disabled;
  return (
    <Button
      // 未武装态就用危险色勾勒（danger-outline），让「删除」的后果一眼可见，
      // 而不是点完第一下才变色（审计 1.13）。
      variant={armed ? tone : tone === "destructive" ? "danger-outline" : "ghost"}
      size="sm"
      disabled={isDisabled}
      onClick={() => {
        if (armed) {
          onConfirm();
          setArmed(false);
        } else {
          setArmed(true);
        }
      }}
    >
      {armed ? (
        <span className="flex items-center gap-1.5">
          <Check className="h-3.5 w-3.5" aria-hidden />
          再次点击确认
        </span>
      ) : loading ? (
        loadingLabel
      ) : (
        label
      )}
    </Button>
  );
}
