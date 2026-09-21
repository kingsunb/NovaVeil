import { useAuth } from "@/store/auth";
import { Button } from "@/components/ui/button";
import { BrandMark } from "@/components/ui/brand-mark";
import { ChangePasswordForm } from "./ChangePasswordForm";

/**
 * 首登强制改密页 —— mustChangePassword=true 时 App 层直接渲染本页，
 * 其余页面一律不可达。这一步必须在路由层挡死：后端此刻对全部业务 API
 * 返回 403，放任进入控制台只会呈现成满屏「加载失败」，把「要去改密码」
 * 误导成网络故障。改密成功后 ChangePasswordForm 回读状态清掉标志，
 * App 自动进入控制台，无需刷新。
 */
export function ForceChangePassword() {
  const { username, logout } = useAuth();
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="glass-panel glass-inset-highlight w-full max-w-md rounded-card p-6">
        <BrandMark size="sm" withName heading className="mb-3" />
        <h2 className="text-sm font-medium text-ink">设置新密码</h2>
        <p className="mt-1 text-xs leading-relaxed text-ink-muted">
          检测到当前使用的是初始密码。为了账户安全，请先设置新密码，
          修改后即可正常使用控制台全部功能。
        </p>
        <div className="mt-4">
          <ChangePasswordForm />
        </div>
        <div className="mt-5 flex items-center justify-between border-t border-border pt-3 text-[11px] text-ink-subtle">
          <span>当前登录：{username || "admin"}</span>
          <Button variant="ghost" size="sm" onClick={() => void logout()}>
            退出登录
          </Button>
        </div>
      </div>
    </div>
  );
}
