import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button } from "./button";

describe("<Button />", () => {
  it("渲染文本并响应 click", async () => {
    const fn = vi.fn();
    render(<Button onClick={fn}>click me</Button>);
    await userEvent.click(screen.getByText("click me"));
    expect(fn).toHaveBeenCalledOnce();
  });

  it("disabled 时不响应 click", async () => {
    const fn = vi.fn();
    render(
      <Button onClick={fn} disabled>
        nope
      </Button>,
    );
    await userEvent.click(screen.getByText("nope"));
    expect(fn).not.toHaveBeenCalled();
  });

  it("默认 type=button，显式 type 优先", () => {
    const { rerender } = render(<Button>ok</Button>);
    expect(screen.getByRole("button")).toHaveAttribute("type", "button");
    rerender(<Button type="submit">ok</Button>);
    expect(screen.getByRole("button")).toHaveAttribute("type", "submit");
  });

  it("primary / ghost / destructive 变体带对应类", () => {
    const { container, rerender } = render(
      <Button variant="primary">a</Button>,
    );
    expect(container.firstChild).toHaveClass("gradient-cta");
    rerender(<Button variant="ghost">a</Button>);
    expect(container.firstChild).toHaveClass("hover:bg-ink/[0.04]");
    rerender(<Button variant="destructive">a</Button>);
    expect(container.firstChild).toHaveClass("bg-destructive");
  });
});
