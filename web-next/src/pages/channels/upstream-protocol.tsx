import { Pill } from "@/components/ui/pill";

const KNOWN_LABELS = {
  chat: "Chat",
  responses: "Responses",
  anthropic: "Anthropic",
} as const;

/**
 * 模型行上游协议的展示文案。
 * 只有 chat / responses / anthropic 有专门名称；空、缺失、0 或其他值都是
 * 「按渠道」，表示按渠道类型转发，不显示成 0 或「不支持」。
 */
export function upstreamProtocolLabel(value: unknown): string {
  if (value === "chat" || value === "responses" || value === "anthropic") {
    return KNOWN_LABELS[value];
  }
  return "按渠道";
}

/**
 * OpenCode 渠道模型行上的上游协议，只读。
 * 调用方只在 channel.opencode_compat 为真时渲染。本组件不提供下拉或保存表单：
 * auto 与 manual 模型都只展示，协议随渠道列表留在 React Query 里。
 */
export function UpstreamProtocolLabel({ protocol }: { protocol?: unknown }) {
  const label = upstreamProtocolLabel(protocol);
  return (
    <span
      className="inline-flex shrink-0"
      title="上游协议，只读。没有单独指定时按渠道类型转发。"
      aria-label={`上游协议 ${label}`}
    >
      <Pill tone="neutral" dot={false}>
        {label}
      </Pill>
    </span>
  );
}
