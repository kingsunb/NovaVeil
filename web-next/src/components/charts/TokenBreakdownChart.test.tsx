import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TokenBreakdownChart } from "./TokenBreakdownChart";

describe("<TokenBreakdownChart /> 渲染", () => {
  it("有数据时渲染环形图与图例", () => {
    render(
      <TokenBreakdownChart
        input={1000}
        output={500}
        reasoning={200}
        cached={100}
      />,
    );
    expect(screen.getByText("总 tokens")).toBeInTheDocument();
    expect(screen.getByText("输入")).toBeInTheDocument();
    expect(screen.getByText("输出")).toBeInTheDocument();
    expect(screen.getByText("推理")).toBeInTheDocument();
    expect(screen.getByText("缓存命中")).toBeInTheDocument();
  });

  it("全零数据时显示空状态", () => {
    render(
      <TokenBreakdownChart
        input={0}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    // 百分比应为 —
    const dashes = screen.getAllByText("—");
    expect(dashes.length).toBeGreaterThanOrEqual(4);
  });
});

describe("<TokenBreakdownChart /> 占比计算", () => {
  it("仅 input 有值时显示 100%", () => {
    render(
      <TokenBreakdownChart
        input={1000}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    expect(screen.getByText("100.0%")).toBeInTheDocument();
    // 其余三类为 0.0%
    const zeros = screen.getAllByText("0.0%");
    expect(zeros.length).toBe(3);
  });

  it("四类均有值时各占比正确", () => {
    // total = 1000, input=400(40%), output=300(30%), reasoning=200(20%), cached=100(10%)
    render(
      <TokenBreakdownChart
        input={400}
        output={300}
        reasoning={200}
        cached={100}
      />,
    );
    expect(screen.getByText("40.0%")).toBeInTheDocument();
    expect(screen.getByText("30.0%")).toBeInTheDocument();
    expect(screen.getByText("20.0%")).toBeInTheDocument();
    expect(screen.getByText("10.0%")).toBeInTheDocument();
  });

  it("仅 output 有值时显示 100%", () => {
    render(
      <TokenBreakdownChart
        input={0}
        output={500}
        reasoning={0}
        cached={0}
      />,
    );
    expect(screen.getByText("100.0%")).toBeInTheDocument();
  });

  it("仅 reasoning 有值时显示 100%", () => {
    render(
      <TokenBreakdownChart
        input={0}
        output={0}
        reasoning={500}
        cached={0}
      />,
    );
    expect(screen.getByText("100.0%")).toBeInTheDocument();
  });

  it("仅 cached 有值时显示 100%", () => {
    render(
      <TokenBreakdownChart
        input={0}
        output={0}
        reasoning={0}
        cached={500}
      />,
    );
    expect(screen.getByText("100.0%")).toBeInTheDocument();
  });
});

describe("<TokenBreakdownChart /> 弧路径", () => {
  it("有数据时渲染 path 弧线", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={500}
        output={300}
        reasoning={0}
        cached={0}
      />,
    );
    const paths = container.querySelectorAll("path");
    // input + output = 2 段弧
    expect(paths.length).toBe(2);
  });

  it("全零数据时不渲染弧线", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={0}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    const paths = container.querySelectorAll("path");
    expect(paths.length).toBe(0);
  });

  it("仅一类有值时渲染单段弧", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={1000}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    const paths = container.querySelectorAll("path");
    expect(paths.length).toBe(1);
  });

  it("四类均有值时渲染四段弧", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={400}
        output={300}
        reasoning={200}
        cached={100}
      />,
    );
    const paths = container.querySelectorAll("path");
    expect(paths.length).toBe(4);
  });

  it("大占比(>50%)时 largeArc 标志正确", () => {
    // input=900, output=100, total=1000 → input 占 90% > 50%, largeArc=1
    const { container } = render(
      <TokenBreakdownChart
        input={900}
        output={100}
        reasoning={0}
        cached={0}
      />,
    );
    const paths = container.querySelectorAll("path");
    // 第一段弧 (input 90%) 应包含 largeArc=1
    const firstPathD = paths[0].getAttribute("d");
    expect(firstPathD).toMatch(/A 60 60 0 1 1/);
  });

  it("小占比(<50%)时 largeArc 标志为 0", () => {
    // input=100, output=100, total=200 → 各 50%, largeArc=0
    const { container } = render(
      <TokenBreakdownChart
        input={100}
        output={100}
        reasoning={0}
        cached={0}
      />,
    );
    const paths = container.querySelectorAll("path");
    const firstPathD = paths[0].getAttribute("d");
    expect(firstPathD).toMatch(/A 60 60 0 0 1/);
  });
});

describe("<TokenBreakdownChart /> 背景圆环", () => {
  it("始终渲染背景 circle 元素", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={0}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    const circles = container.querySelectorAll("circle");
    expect(circles.length).toBe(1);
  });

  it("svg 有 role=img 和 aria-label", () => {
    const { container } = render(
      <TokenBreakdownChart
        input={100}
        output={0}
        reasoning={0}
        cached={0}
      />,
    );
    const svg = container.querySelector("svg");
    expect(svg?.getAttribute("role")).toBe("img");
    expect(svg?.getAttribute("aria-label")).toBe("Token 构成图");
  });
});
