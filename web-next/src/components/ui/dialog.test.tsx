import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Dialog, DialogContent } from "./dialog";

describe("<DialogContent> variants", () => {
  it("variant=sheet 渲染", () => {
    render(
      <Dialog open>
        <DialogContent variant="sheet" data-testid="dc">
          sheet content
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByText("sheet content")).toBeInTheDocument();
  });

  it("variant=fullscreen 渲染", () => {
    render(
      <Dialog open>
        <DialogContent variant="fullscreen" data-testid="dc">
          fullscreen content
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByText("fullscreen content")).toBeInTheDocument();
  });

  it("variant=wide 渲染", () => {
    render(
      <Dialog open>
        <DialogContent variant="wide" data-testid="dc">
          wide content
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByText("wide content")).toBeInTheDocument();
  });

  it("size=sm dialog 渲染", () => {
    render(
      <Dialog open>
        <DialogContent variant="dialog" size="sm">
          small dialog
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByText("small dialog")).toBeInTheDocument();
  });

  it("size=lg dialog 渲染", () => {
    render(
      <Dialog open>
        <DialogContent variant="dialog" size="lg">
          large dialog
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByText("large dialog")).toBeInTheDocument();
  });

  it("dialog 短视口留边距并限制高度", () => {
    render(
      <Dialog open>
        <DialogContent variant="dialog" data-testid="dialog-box">
          dialog body
        </DialogContent>
      </Dialog>,
    );
    const box = screen.getByTestId("dialog-box");
    expect(box).toHaveClass("w-[calc(100vw-2rem)]");
    expect(box).toHaveClass("max-h-[min(90dvh,calc(100dvh-2rem))]");
    expect(box).toHaveClass("overflow-hidden", "flex", "flex-col");
  });

  it("wide 变体移动端全宽、桌面八成宽", () => {
    render(
      <Dialog open>
        <DialogContent variant="wide" data-testid="wide-box">
          wide body
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByTestId("wide-box")).toHaveClass("w-full", "sm:w-4/5");
  });
});
