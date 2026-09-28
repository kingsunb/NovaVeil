import { Pill, type PillTone } from "@/components/ui/pill";
import type { EvalOutcome } from "@/lib/model-eval";

const outcomes: Record<EvalOutcome, { label: string; tone: PillTone }> = {
  ok: { label: "格式合规", tone: "success" },
  violation: { label: "格式不符", tone: "warning" },
  error: { label: "请求失败", tone: "danger" },
  manual: { label: "手动加入", tone: "info" },
};

export function EvalOutcomeBadge({ outcome }: { outcome: EvalOutcome }) {
  const { label, tone } = outcomes[outcome];
  return <Pill tone={tone} className="shrink-0 whitespace-nowrap">{label}</Pill>;
}
