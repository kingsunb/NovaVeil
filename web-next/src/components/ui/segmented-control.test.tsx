import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SegmentedControl } from "./segmented-control";

describe("<SegmentedControl />", () => {
  it("渲染全部选项并标记当前项", () => {
    render(
      <SegmentedControl
        value="on"
        onChange={() => {}}
        options={[
          { value: "all", label: "全部" },
          { value: "on", label: "启用" },
          { value: "off", label: "停用" },
        ]}
      />,
    );
    expect(screen.getByRole("button", { name: "启用" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "全部" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("点击未选项调用 onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <SegmentedControl
        value="all"
        onChange={onChange}
        options={[
          { value: "all", label: "全部" },
          { value: "on", label: "启用" },
        ]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "启用" }));
    expect(onChange).toHaveBeenCalledWith("on");
  });

  it("按钮带 focus-visible ring 类", () => {
    render(
      <SegmentedControl
        value="all"
        onChange={() => {}}
        options={[{ value: "all", label: "全部" }]}
      />,
    );
    expect(screen.getByRole("button", { name: "全部" })).toHaveClass(
      "focus-visible:ring-2",
    );
  });
});
