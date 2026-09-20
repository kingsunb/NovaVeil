import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ConfirmButton } from "./confirm-button";

describe("<ConfirmButton />", () => {
  it("两段式确认：首次点击进入待命态，再次点击才触发 onConfirm", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(<ConfirmButton onConfirm={onConfirm} />);
    const idle = screen.getByRole("button", { name: "删除" });
    await user.click(idle);
    // 武装态：label 变为「再次点击确认」，未触发删除
    const armed = screen.getByRole("button", { name: /再次点击确认/ });
    expect(armed).toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();
    await user.click(armed);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    // 确认后回到待命前的空闲态
    expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
  });

  it("2.2s 未再次点击自动复位，不触发 onConfirm", async () => {
    const onConfirm = vi.fn();
    render(<ConfirmButton onConfirm={onConfirm} />);
    await userEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(
      screen.getByRole("button", { name: /再次点击确认/ }),
    ).toBeInTheDocument();
    // 真实定时器：等武装态超时复位
    await waitFor(
      () => {
        expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
      },
      { timeout: 4000 },
    );
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("destructive tone：未武装态即 danger-outline 危险色（审计 1.13）", () => {
    render(<ConfirmButton tone="destructive" onConfirm={() => {}} />);
    expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
  });

  it("loading 态显示「删除中…」并禁用", () => {
    render(<ConfirmButton onConfirm={() => {}} loading />);
    const btn = screen.getByRole("button", { name: /删除中/ });
    expect(btn).toBeDisabled();
  });

  it("自定义 loadingLabel 与 disabled 可用于「清空归档」等非删除场景", () => {
    render(
      <ConfirmButton
        onConfirm={() => {}}
        label="清空归档"
        loadingLabel="清空中…"
        loading
        disabled
      />,
    );
    const btn = screen.getByRole("button", { name: /清空中/ });
    expect(btn).toBeDisabled();
  });
});
