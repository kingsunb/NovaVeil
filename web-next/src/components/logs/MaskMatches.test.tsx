import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { MaskMatches } from "./MaskMatches";
import type { MaskMatchSummary } from "@/lib/types";

// 实施边界修订(文档 07): 日志命中明细只含 label + placeholder, 不含 original。
const matches: MaskMatchSummary[] = [
  { label: "PHONE", placeholder: "{{PHONE_bcdfgh}}" },
  { label: "EMAIL", placeholder: "{{EMAIL_ab12cd}}" },
  { label: "SECRET", placeholder: "{{SECRET_xy99z1}}" },
  { label: "TERM", placeholder: "{{TERM_qw3rty}}" },
];

describe("<MaskMatches />", () => {
  it("空数组 / undefined / null → 不渲染任何区域", () => {
    const { container } = render(<MaskMatches matches={[]} />);
    expect(container.querySelector('[data-testid="mask-matches"]')).toBeNull();
    const { container: c2 } = render(<MaskMatches matches={undefined} />);
    expect(c2.querySelector('[data-testid="mask-matches"]')).toBeNull();
    const { container: c3 } = render(<MaskMatches matches={null} />);
    expect(c3.querySelector('[data-testid="mask-matches"]')).toBeNull();
  });

  it("有命中时渲染标题、计数与每条规则标签/占位符", () => {
    render(<MaskMatches matches={matches} />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("脱敏命中")).toBeTruthy();
    expect(within(box).getByText("仅本请求命中明细")).toBeTruthy();
    expect(within(box).getByText(String(matches.length))).toBeTruthy();
    // 占位符可见
    for (const m of matches) {
      expect(within(box).getByText(m.placeholder)).toBeTruthy();
    }
    // 规则标签 Pill 可见
    expect(within(box).getByText("PHONE")).toBeTruthy();
    expect(within(box).getByText("SECRET")).toBeTruthy();
  });

  it("边界修订: 不渲染命中原文 / 不提供「显示原文」入口", () => {
    render(<MaskMatches matches={matches} />);
    const box = screen.getByTestId("mask-matches");
    // 无展开/收起交互
    expect(within(box).queryByText("显示原文")).toBeNull();
    expect(within(box).queryByText("收起")).toBeNull();
  });

  it("支持自定义标题（如错误日志的「触发规则」）", () => {
    render(<MaskMatches matches={matches} title="触发规则" caption="" />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("触发规则")).toBeTruthy();
    // caption 传空串 → 不渲染标注
    expect(within(box).queryByText("仅本请求命中明细")).toBeNull();
  });
});
