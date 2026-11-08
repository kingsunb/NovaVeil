import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TokenBreakdownChart } from "./TokenBreakdownChart";

describe("<TokenBreakdownChart /> 渲染", () => {
  it("有数据时渲染环形图与两类图例", () => {
    render(<TokenBreakdownChart input={1000} output={500} />);
    expect(screen.getByText("总 tokens")).toBeInTheDocument();
    expect(screen.getByText("输入")).toBeInTheDocument();
    expect(screen.getByText("输出")).toBeInTheDocument();
    expect(screen.queryByText("推理")).toBeNull();
    expect(screen.queryByText("缓存命中")).toBeNull();
  });

  it("全零数据时显示空状态", () => {
    render(<TokenBreakdownChart input={0} output={0} />);
    expect(screen.getAllByText("—")).toHaveLength(2);
  });

  it("负数归零后按剩余有效 Token 计算占比", () => {
    const { container } = render(
      <TokenBreakdownChart input={-100} output={200} />,
    );
    expect(screen.getByText("100.0%")).toBeInTheDocument();
    expect(screen.getByText("0.0%")).toBeInTheDocument();
    expect(screen.getAllByText("200")).toHaveLength(2);
    expect(container.querySelectorAll("path")).toHaveLength(1);
  });

  it("非有限输入归零并显示空状态", () => {
    const { container } = render(
      <TokenBreakdownChart input={Number.NaN} output={Number.POSITIVE_INFINITY} />,
    );
    expect(screen.getAllByText("0")).toHaveLength(3);
    expect(screen.getAllByText("—")).toHaveLength(2);
    expect(container.querySelectorAll("path")).toHaveLength(0);
  });
});

describe("<TokenBreakdownChart /> 占比计算", () => {
  it("仅 input 有值时显示 100%", () => {
    render(<TokenBreakdownChart input={1000} output={0} />);
    expect(screen.getByText("100.0%")).toBeInTheDocument();
    expect(screen.getAllByText("0.0%")).toHaveLength(1);
  });

  it("两类均有值时各占比正确", () => {
    render(<TokenBreakdownChart input={600} output={400} />);
    expect(screen.getByText("60.0%")).toBeInTheDocument();
    expect(screen.getByText("40.0%")).toBeInTheDocument();
  });

  it("仅 output 有值时显示 100%", () => {
    render(<TokenBreakdownChart input={0} output={500} />);
    expect(screen.getByText("100.0%")).toBeInTheDocument();
  });
});

describe("<TokenBreakdownChart /> 弧路径", () => {
  it("有数据时渲染 path 弧线", () => {
    const { container } = render(
      <TokenBreakdownChart input={500} output={300} />,
    );
    expect(container.querySelectorAll("path")).toHaveLength(2);
  });

  it("全零数据时不渲染弧线", () => {
    const { container } = render(<TokenBreakdownChart input={0} output={0} />);
    expect(container.querySelectorAll("path")).toHaveLength(0);
  });

  it("仅一类有值时渲染完整圆环", () => {
    const { container } = render(
      <TokenBreakdownChart input={1000} output={0} />,
    );
    const path = container.querySelector("path");
    expect(container.querySelectorAll("path")).toHaveLength(1);
    expect(path?.getAttribute("d")?.match(/A 60 60 0 1 1/g)).toHaveLength(2);
  });

  it("大占比(>50%)时 largeArc 标志正确", () => {
    // input=900, output=100, total=1000 → input 占 90% > 50%, largeArc=1
    const { container } = render(
      <TokenBreakdownChart input={900} output={100} />,
    );
    const firstPathD = container.querySelectorAll("path")[0].getAttribute("d");
    expect(firstPathD).toMatch(/A 60 60 0 1 1/);
  });

  it("小占比(<50%)时 largeArc 标志为 0", () => {
    // input=100, output=100, total=200 → 各 50%, largeArc=0
    const { container } = render(
      <TokenBreakdownChart input={100} output={100} />,
    );
    const firstPathD = container.querySelectorAll("path")[0].getAttribute("d");
    expect(firstPathD).toMatch(/A 60 60 0 0 1/);
  });
});

describe("<TokenBreakdownChart /> 背景圆环", () => {
  it("始终渲染背景 circle 元素", () => {
    const { container } = render(<TokenBreakdownChart input={0} output={0} />);
    expect(container.querySelectorAll("circle")).toHaveLength(1);
  });

  it("svg 有 role=img 和 aria-label", () => {
    const { container } = render(
      <TokenBreakdownChart input={100} output={0} />,
    );
    const svg = container.querySelector("svg");
    expect(svg?.getAttribute("role")).toBe("img");
    expect(svg?.getAttribute("aria-label")).toBe("Token 构成图");
  });
});
