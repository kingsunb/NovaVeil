import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { LoginSkeleton, LogsSkeleton, SettingsSkeleton, TableSkeleton } from "./skeleton";

describe("<Skeleton /> 族", () => {
  it("LoginSkeleton：居中卡片占位，不引入可交互表单", () => {
    const { container } = render(<LoginSkeleton />);
    expect(screen.getByRole("status", { name: "加载中" })).toHaveClass("max-w-[380px]");
    expect(container.firstChild).toHaveClass("items-center", "justify-center");
    expect(container.querySelector("input, button, form")).toBeNull();
  });

  it("SettingsSkeleton：单个 status 区域（左导航 + 表单骨架）", () => {
    render(<SettingsSkeleton />);
    expect(screen.getByRole("status", { name: "加载中" })).toBeInTheDocument();
  });

  it("LogsSkeleton：外层 + 内嵌 TableSkeleton 共两个 status 区域", () => {
    render(<LogsSkeleton />);
    expect(screen.getAllByRole("status", { name: "加载中" })).toHaveLength(2);
  });

  it("TableSkeleton：默认与自定义行数都可渲染", () => {
    const { container: a } = render(<TableSkeleton />);
    expect(a.querySelector('[role="status"]')).not.toBeNull();
    const { container: b } = render(<TableSkeleton rows={1} />);
    expect(b.querySelector('[role="status"]')).not.toBeNull();
  });
});
