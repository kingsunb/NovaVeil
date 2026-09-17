import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { MaskMatches } from "./MaskMatches";
import type { MaskMatchSummary } from "@/lib/types";

// 决策变更(文档 07): 日志命中明细含 label + original + placeholder, 展示原文。
const matches: MaskMatchSummary[] = [
  { label: "PHONE", original: "13800138000", placeholder: "{{PHONE_bcdfgh}}" },
  { label: "EMAIL", original: "a@example.com", placeholder: "{{EMAIL_ab12cd}}" },
  { label: "SECRET", original: "sk-abc123", placeholder: "{{SECRET_xy99z1}}" },
  { label: "TERM", original: "内部代号A", placeholder: "{{TERM_qw3rty}}" },
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

  it("有命中时渲染标题、计数与每条规则标签/原文/占位符", () => {
    render(<MaskMatches matches={matches} />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("脱敏命中")).toBeTruthy();
    expect(within(box).getByText("仅本请求命中明细")).toBeTruthy();
    expect(within(box).getByText(String(matches.length))).toBeTruthy();
    // 占位符与原文均可见
    for (const m of matches) {
      expect(within(box).getByText(m.placeholder)).toBeTruthy();
      expect(within(box).getByText(m.original)).toBeTruthy();
    }
    // 规则标签 Pill 可见
    expect(within(box).getByText("PHONE")).toBeTruthy();
    expect(within(box).getByText("SECRET")).toBeTruthy();
  });

  it("命中原文字段缺省时仍渲染标签与占位符", () => {
    render(
      <MaskMatches
        matches={[{ label: "PHONE", original: "", placeholder: "{{PHONE_bcdfgh}}" }]}
      />,
    );
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("{{PHONE_bcdfgh}}")).toBeTruthy();
    // 空原文不渲染「原文」标注
    expect(within(box).queryByText("原文")).toBeNull();
  });

  it("支持自定义标题（如错误日志的「触发规则」）", () => {
    render(<MaskMatches matches={matches} title="触发规则" caption="" />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("触发规则")).toBeTruthy();
    // caption 传空串 → 不渲染标注
    expect(within(box).queryByText("仅本请求命中明细")).toBeNull();
  });

  it("truncated=true 渲染截断提示", () => {
    render(<MaskMatches matches={matches} truncated />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("命中明细已截断，仅展示部分命中")).toBeTruthy();
  });

  it("truncated 未传 / false → 不渲染截断提示", () => {
    const { container: c1 } = render(<MaskMatches matches={matches} />);
    expect(within(c1).queryByText("命中明细已截断，仅展示部分命中")).toBeNull();
    const { container: c2 } = render(<MaskMatches matches={matches} truncated={false} />);
    expect(within(c2).queryByText("命中明细已截断，仅展示部分命中")).toBeNull();
  });
});
