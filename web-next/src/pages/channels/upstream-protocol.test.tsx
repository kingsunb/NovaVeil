import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import {
  UpstreamProtocolLabel,
  upstreamProtocolLabel,
} from "./upstream-protocol";

describe("upstreamProtocolLabel", () => {
  it("已知协议用周围已有的协议名", () => {
    expect(upstreamProtocolLabel("chat")).toBe("Chat");
    expect(upstreamProtocolLabel("responses")).toBe("Responses");
    expect(upstreamProtocolLabel("anthropic")).toBe("Anthropic");
  });

  it("空、缺失、0 和不支持都显示按渠道，不当成错误", () => {
    expect(upstreamProtocolLabel("")).toBe("按渠道");
    expect(upstreamProtocolLabel(undefined)).toBe("按渠道");
    expect(upstreamProtocolLabel(null)).toBe("按渠道");
    expect(upstreamProtocolLabel(0)).toBe("按渠道");
    expect(upstreamProtocolLabel("0")).toBe("按渠道");
    expect(upstreamProtocolLabel("不支持")).toBe("按渠道");
  });
});

describe("<UpstreamProtocolLabel />", () => {
  it("只读展示文案，不提供保存协议的表单", () => {
    const { rerender } = render(<UpstreamProtocolLabel protocol="responses" />);
    expect(screen.getByLabelText("上游协议 Responses")).toHaveTextContent(
      "Responses",
    );
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();

    rerender(<UpstreamProtocolLabel />);
    expect(screen.getByLabelText("上游协议 按渠道")).toHaveTextContent("按渠道");
    expect(screen.queryByText("0")).not.toBeInTheDocument();
    expect(screen.queryByText("不支持")).not.toBeInTheDocument();

    rerender(<UpstreamProtocolLabel protocol={0} />);
    expect(screen.getByLabelText("上游协议 按渠道")).toBeInTheDocument();
    expect(screen.queryByText("0")).not.toBeInTheDocument();
    expect(screen.queryByText("不支持")).not.toBeInTheDocument();
  });
});
