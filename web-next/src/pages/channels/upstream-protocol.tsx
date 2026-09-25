import { Pill } from "@/components/ui/pill";
import { upstreamProtocolLabel } from "./upstream-protocol-label";

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
