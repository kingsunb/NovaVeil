import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import { MaskMatches } from "./MaskMatches";
import type { MaskTestMatch } from "@/lib/types";

const matches: MaskTestMatch[] = [
  { label: "PHONE", original: "13800138000", placeholder: "{{PHONE_bcdfgh}}" },
  { label: "EMAIL", original: "user@example.com", placeholder: "{{EMAIL_ab12cd}}" },
  { label: "SECRET", original: "sk-super-secret-key", placeholder: "{{SECRET_xy99z1}}" },
  { label: "TERM", original: "内部代号X", placeholder: "{{TERM_qw3rty}}" },
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

  it("有命中时渲染标题、计数与每条占位符；原文默认折叠", () => {
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
    // 原文默认折叠：不直接展示，仅给「显示原文」按钮
    expect(within(box).queryByText("13800138000")).toBeNull();
    expect(within(box).getAllByText("显示原文")).toHaveLength(matches.length);
  });

  it("点击「显示原文」展开命中原文，再点「收起」折叠", () => {
    render(<MaskMatches matches={matches} />);
    const box = screen.getByTestId("mask-matches");
    const expandBtns = within(box).getAllByText("显示原文");
    fireEvent.click(expandBtns[0]!);
    // 第一条原文展开可见
    expect(within(box).getByText("13800138000")).toBeTruthy();
    // 其余仍折叠
    expect(within(box).queryByText("user@example.com")).toBeNull();
    // 收起
    fireEvent.click(within(box).getByText("收起"));
    expect(within(box).queryByText("13800138000")).toBeNull();
  });

  it("支持自定义标题（如错误日志的「触发规则」）", () => {
    render(<MaskMatches matches={matches} title="触发规则" caption="" />);
    const box = screen.getByTestId("mask-matches");
    expect(within(box).getByText("触发规则")).toBeTruthy();
    // caption 传空串 → 不渲染标注
    expect(within(box).queryByText("仅本请求命中明细")).toBeNull();
  });
});
