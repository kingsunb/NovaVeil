import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChangePasswordForm } from "./ChangePasswordForm";

const refreshStatus = vi.fn();
const changePassword = vi.fn();

vi.mock("@/store/auth", () => ({
  useAuth: () => ({ refreshStatus }),
}));
vi.mock("@/lib/api", () => ({
  api: { changePassword: (...args: unknown[]) => changePassword(...args) },
}));

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe("<ChangePasswordForm /> 新密码二次确认", () => {
  beforeEach(() => {
    refreshStatus.mockClear();
    changePassword.mockClear();
    changePassword.mockResolvedValue(null);
  });

  it("两次输入不一致时禁用提交并展示错误", async () => {
    const user = userEvent.setup();
    render(<ChangePasswordForm />, { wrapper: Wrapper });

    const old = screen.getByPlaceholderText("当前密码");
    const next = screen.getByPlaceholderText("新密码（≥8 位）");
    const confirm = screen.getByPlaceholderText("确认新密码");
    const submit = screen.getByRole("button", { name: "更新密码" });

    await user.type(old, "old-password");
    await user.type(next, "new-password-123");
    await user.type(confirm, "new-password-456");

    expect(screen.getByRole("alert")).toHaveTextContent("两次输入的新密码不一致");
    expect(submit).toBeDisabled();

    await user.clear(confirm);
    await user.type(confirm, "new-password-123");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(submit).toBeEnabled();
  });

  it("确认一致后提交并刷新登录态，成功后清空三个字段", async () => {
    const user = userEvent.setup();
    render(<ChangePasswordForm />, { wrapper: Wrapper });

    await user.type(screen.getByPlaceholderText("当前密码"), "old-password");
    await user.type(screen.getByPlaceholderText("新密码（≥8 位）"), "new-password-123");
    await user.type(screen.getByPlaceholderText("确认新密码"), "new-password-123");

    const submit = screen.getByRole("button", { name: "更新密码" });
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() => {
      expect(changePassword).toHaveBeenCalledWith("old-password", "new-password-123");
    });
    expect(refreshStatus).toHaveBeenCalled();

    await waitFor(() => {
      expect(screen.getByPlaceholderText("当前密码")).toHaveValue("");
      expect(screen.getByPlaceholderText("新密码（≥8 位）")).toHaveValue("");
      expect(screen.getByPlaceholderText("确认新密码")).toHaveValue("");
    });
  });
});
