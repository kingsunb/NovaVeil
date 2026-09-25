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
