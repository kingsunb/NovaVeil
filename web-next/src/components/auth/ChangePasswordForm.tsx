import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/lib/api";
import { useAuth } from "@/store/auth";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/**
 * 修改密码表单 —— 「设置 → 账户」与首登强制改密页共用。
 * 改密成功后立即 refreshStatus() 回读 must_change_password：
 * 强制改密门靠这个标志放行，设置页的引导横幅也随它消失，
 * 两个场景都不需要用户手动刷新页面。
 */
export function ChangePasswordForm() {
  const { refreshStatus } = useAuth();
  const [old, setOld] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const confirmMismatch = next.length >= 8 && confirm.length > 0 && next !== confirm;
  const canSubmit = !!old && next.length >= 8 && confirm === next;
  const mut = useMutation({
    mutationFn: () => api.changePassword(old, next),
    onSuccess: () => {
      toast.success("密码已更新");
      setOld("");
      setNext("");
      setConfirm("");
      void refreshStatus();
    },
    onError: (e: Error) => toast.error(e.message || "修改失败"),
  });
  return (
    <div className="space-y-2">
      <p className="text-xs font-medium text-ink-muted">修改密码</p>
      <div className="grid grid-cols-2 gap-2">
        <Input
          type="password"
          value={old}
          onChange={(e) => setOld(e.target.value)}
          placeholder="当前密码"
          autoComplete="current-password"
        />
        <Input
          type="password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          placeholder="新密码（≥8 位）"
          autoComplete="new-password"
        />
        <Input
          type="password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder="确认新密码"
          autoComplete="new-password"
          invalid={confirmMismatch}
          aria-invalid={confirmMismatch || undefined}
          aria-label="确认新密码"
        />
      </div>
      {confirmMismatch && (
        <p role="alert" className="text-xs text-destructive">
          两次输入的新密码不一致
        </p>
      )}
      <div className="flex justify-end">
        <Button
          variant="primary"
          size="sm"
          loading={mut.isPending}
          disabled={!canSubmit}
          onClick={() => mut.mutate()}
        >
          更新密码
        </Button>
      </div>
    </div>
  );
}
