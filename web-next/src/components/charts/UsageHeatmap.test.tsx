import { describe, expect, it, vi, afterEach } from "vitest";
import { render, fireEvent, screen } from "@testing-library/react";
import { UsageHeatmap } from "./UsageHeatmap";
import type { UsageHeatmapPoint } from "@/lib/types";

/** 构造测试数据：最近 N 天，每天有指定 token 量。 */
function makeHeatmapData(days: number, tokensPerDay: number, cost = 0): UsageHeatmapPoint[] {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return Array.from({ length: days }).map((_, i) => {
    const d = new Date(today);
    d.setDate(today.getDate() - (days - 1 - i));
    return {
      date: d.toISOString().slice(0, 10),
      tokens: tokensPerDay,
      cost,
      count: 10,
    };
  });
}

/** 构造单条指定日期的数据。 */
function singlePoint(date: string, tokens: number, cost = 0, count = 5): UsageHeatmapPoint {
  return { date, tokens, cost, count };
}

/** 获取今天的日期字符串。 */
function todayStr(): string {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return today.toISOString().slice(0, 10);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("<UsageHeatmap /> 渲染", () => {
  it("有数据时渲染热力图与统计摘要", () => {
    const data = makeHeatmapData(30, 1000);
    render(<UsageHeatmap data={data} />);

    expect(screen.getByText("活跃天数")).toBeInTheDocument();
    expect(screen.getByText(/总计/)).toBeInTheDocument();
    expect(screen.getByText("少")).toBeInTheDocument();
    expect(screen.getByText("多")).toBeInTheDocument();
  });

  it("空数据时仍渲染框架", () => {
    render(<UsageHeatmap data={[]} />);
    expect(screen.getByText("活跃天数")).toBeInTheDocument();
    expect(screen.getByText("少")).toBeInTheDocument();
  });

  it("有 cost 数据时显示预计消耗", () => {
    const data = [singlePoint(todayStr(), 10000, 1.5)];
    render(<UsageHeatmap data={data} weeks={4} />);
    expect(screen.getByText(/预计消耗/)).toBeInTheDocument();
    expect(screen.getByText("$1.50")).toBeInTheDocument();
  });

  it("cost 全零时不显示预计消耗", () => {
    const data = makeHeatmapData(10, 1000, 0);
    render(<UsageHeatmap data={data} weeks={4} />);
    expect(screen.queryByText(/预计消耗/)).not.toBeInTheDocument();
  });

  it("全零数据时活跃天数为 0 且无预计消耗", () => {
    const data = makeHeatmapData(10, 0);
    render(<UsageHeatmap data={data} />);
    expect(screen.getByText("活跃天数")).toBeInTheDocument();
    expect(screen.queryByText(/预计消耗/)).not.toBeInTheDocument();
  });
});

describe("<UsageHeatmap /> 颜色级别 (getLevel)", () => {
  it("不同用量比例渲染不同颜色级别", () => {
    // max=1000, 构造 4 天分别触发 level 1/2/3/4
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const makePoint = (daysAgo: number, tokens: number): UsageHeatmapPoint => {
      const d = new Date(today);
      d.setDate(today.getDate() - daysAgo);
      return { date: d.toISOString().slice(0, 10), tokens, cost: 0, count: 1 };
    };
    // level 1: 100/1000=0.1 < 0.25
    // level 2: 400/1000=0.4 < 0.5
    // level 3: 600/1000=0.6 < 0.75
    // level 4: 900/1000=0.9 >= 0.75
    const data = [
      makePoint(3, 100),
      makePoint(2, 400),
      makePoint(1, 600),
      makePoint(0, 900),
    ];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);

    // 所有 rect 元素
    const rects = container.querySelectorAll("rect");
    expect(rects.length).toBeGreaterThan(0);

    // 收集所有 class 中包含 emerald 的 rect（有数据的格子）
    const emeraldRects = Array.from(rects).filter((r) =>
      r.getAttribute("class")?.includes("emerald"),
    );
    // 应该有 4 种不同的颜色级别
    const uniqueClasses = new Set(emeraldRects.map((r) => r.getAttribute("class")));
    expect(uniqueClasses.size).toBe(4);
  });

  it("零 token 的格子使用 level 0 颜色", () => {
    const data = [singlePoint(todayStr(), 0)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);
    const rects = container.querySelectorAll("rect");
    // 至少有一个 rect 使用 surface-subtle (level 0)
    const level0Rects = Array.from(rects).filter((r) =>
      r.getAttribute("class")?.includes("surface-subtle"),
    );
    expect(level0Rects.length).toBeGreaterThan(0);
  });
});

describe("<UsageHeatmap /> hover 交互", () => {
  /** 获取 tooltip 元素（class 含 pointer-events-none 的 div） */
  function getTooltip(container: HTMLElement): HTMLElement | null {
    return container.querySelector(".pointer-events-none");
  }

  /** 找到有数据的 rect（class 含 emerald） */
  function findDataRect(container: HTMLElement): Element {
    const rects = container.querySelectorAll("rect");
    const dataRect = Array.from(rects).find((r) =>
      r.getAttribute("class")?.includes("emerald"),
    );
    expect(dataRect).toBeDefined();
    return dataRect!;
  }

  /** 找到无数据的 rect（class 含 surface-subtle） */
  function findEmptyRect(container: HTMLElement): Element {
    const rects = container.querySelectorAll("rect");
    const emptyRect = Array.from(rects).find((r) =>
      r.getAttribute("class")?.includes("surface-subtle"),
    );
    expect(emptyRect).toBeDefined();
    return emptyRect!;
  }

  it("hover 有数据的格子时显示 tooltip", () => {
    const data = [singlePoint(todayStr(), 5000, 2.0, 20)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);
    const dataRect = findDataRect(container);

    // hover 前 tooltip 不存在
    expect(getTooltip(container)).toBeNull();

    // mouse enter → tooltip 出现
    fireEvent.mouseEnter(dataRect);
    const tooltip = getTooltip(container);
    expect(tooltip).not.toBeNull();
    expect(tooltip!.textContent).toContain("20 请求");

    // mouse leave → tooltip 消失
    fireEvent.mouseLeave(dataRect);
    expect(getTooltip(container)).toBeNull();
  });

  it("hover 有 cost 的格子时 tooltip 显示消耗", () => {
    const data = [singlePoint(todayStr(), 5000, 3.5, 10)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);
    const dataRect = findDataRect(container);

    fireEvent.mouseEnter(dataRect);
    const tooltip = getTooltip(container);
    expect(tooltip).not.toBeNull();
    expect(tooltip!.textContent).toContain("$3.5000");
  });

  it("hover cost=0 的格子时 tooltip 不显示消耗", () => {
    const data = [singlePoint(todayStr(), 5000, 0, 10)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);
    const dataRect = findDataRect(container);

    fireEvent.mouseEnter(dataRect);
    const tooltip = getTooltip(container);
    expect(tooltip).not.toBeNull();
    expect(tooltip!.textContent).toContain("10 请求");
    expect(tooltip!.textContent).not.toContain("$0.0000");
  });

  it("hover 无数据的格子时不显示 tooltip", () => {
    const data = [singlePoint(todayStr(), 5000, 1.0, 5)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);
    const emptyRect = findEmptyRect(container);

    fireEvent.mouseEnter(emptyRect);
    expect(getTooltip(container)).toBeNull();
  });
});

describe("<UsageHeatmap /> title 元素", () => {
  it("有数据的格子 title 包含 token 信息", () => {
    const data = [singlePoint(todayStr(), 5000, 1.0, 10)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);

    const titles = container.querySelectorAll("title");
    const dataTitle = Array.from(titles).find((t) =>
      t.textContent?.includes("tokens"),
    );
    expect(dataTitle).toBeDefined();
  });

  it("无数据的格子 title 显示「无数据」", () => {
    const data = [singlePoint(todayStr(), 5000, 1.0, 10)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);

    const titles = container.querySelectorAll("title");
    const emptyTitle = Array.from(titles).find((t) =>
      t.textContent?.includes("无数据"),
    );
    expect(emptyTitle).toBeDefined();
  });
});

describe("<UsageHeatmap /> 月份标签", () => {
  it("足够多的周数时渲染多个不同月份标签", () => {
    const data = makeHeatmapData(90, 1000);
    const { container } = render(<UsageHeatmap data={data} weeks={14} />);

    // 月份标签是 <text> 元素，内容以 "月" 结尾
    const monthTexts = Array.from(container.querySelectorAll("text"))
      .map((t) => t.textContent)
      .filter((c) => c?.includes("月"));
    // 90 天跨越至少 2-3 个月
    expect(monthTexts.length).toBeGreaterThanOrEqual(2);
  });

  it("少量数据时仍能渲染月份标签", () => {
    const data = [singlePoint(todayStr(), 100)];
    const { container } = render(<UsageHeatmap data={data} weeks={4} />);

    const monthTexts = Array.from(container.querySelectorAll("text"))
      .map((t) => t.textContent)
      .filter((c) => c?.includes("月"));
    expect(monthTexts.length).toBeGreaterThanOrEqual(1);
  });
});

describe("<UsageHeatmap /> 星期标签", () => {
  it("渲染周一/三/五标签", () => {
    render(<UsageHeatmap data={[]} />);
    expect(screen.getByText("一")).toBeInTheDocument();
    expect(screen.getByText("三")).toBeInTheDocument();
    expect(screen.getByText("五")).toBeInTheDocument();
  });
});

describe("<UsageHeatmap /> 图例", () => {
  it("渲染 5 个颜色级别图例方块", () => {
    const { container } = render(<UsageHeatmap data={[]} />);
    // 图例区域在 "少" 和 "多" 之间，有 5 个 span
    const legend = screen.getByText("少").parentElement;
    const spans = legend?.querySelectorAll("span");
    // "少" + 5 色块 + "多" = 7 spans
    expect(spans?.length).toBe(7);
  });
});
