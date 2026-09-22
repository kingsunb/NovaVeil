import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { BrandMark } from "@/components/ui/brand-mark";
import { useAuth } from "@/store/auth";
import { APIError } from "@/lib/api";

/**
 * 登录页 —— 居中磨砂卡片，品牌标识保持清晰。
 */
export default function LoginPage() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const usernameRef = useRef<HTMLInputElement | null>(null);

  // 不用 autoFocus：懒加载 chunk 挂载完成时机不定，autoFocus 会在 React
  // 完成挂载前抢占焦点；这里在组件自身 effect 中聚焦（审计 4.21）。
  useEffect(() => {
    usernameRef.current?.focus();
  }, []);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await login(username, password, remember);
      toast.success(`欢迎回来，${username}`);
      navigate("/dashboard", { replace: true });
    } catch (err) {
      if (err instanceof APIError) {
        if (err.status === 401) setError("用户名或密码错误");
        else if (err.status === 429) setError("登录尝试过于频繁，请稍后再试");
        else setError(err.statusText || "登录失败");
      } else {
        setError(err instanceof Error ? err.message : "登录失败");
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="bg-gradient-subtle relative flex min-h-full items-center justify-center overflow-hidden bg-background px-4">
      <form
        onSubmit={onSubmit}
        className="glass-panel glass-inset-highlight w-full max-w-[380px] rounded-card p-8 shadow-apple-lg"
      >
        <div className="mb-8">
          <BrandMark
            size="lg"
            withName
            stacked
            heading
            caption="LLM API 网关控制台"
            className="items-center"
          />
        </div>

        <div className="space-y-4">
          <label className="block">
            <span className="mb-1.5 block text-xs font-medium text-ink-muted">
              用户名
            </span>
            <Input
              ref={usernameRef}
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              className="h-10"
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-xs font-medium text-ink-muted">
              密码
            </span>
            <Input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              className="h-10"
            />
          </label>

          <label
            htmlFor="remember-device"
            className="flex cursor-pointer items-center gap-2 text-[13px] text-ink-muted"
          >
            <input
              id="remember-device"
              type="checkbox"
              checked={remember}
              onChange={(e) => setRemember(e.target.checked)}
              aria-label="信任此设备（24 小时内免登录）"
              className="h-4 w-4 rounded-md border-border/60 accent-[#007AFF]"
            />
            信任此设备（24 小时内免登录）
          </label>

          {error && (
            <p
              role="alert"
              className="rounded-lg border border-destructive/20 bg-destructive/[0.04] px-3 py-2 text-[13px] text-destructive"
            >
              {error}
            </p>
          )}

          <Button
            type="submit"
            variant="primary"
            size="lg"
            disabled={loading}
            className="mt-2 w-full"
          >
            {loading ? "登录中…" : "登录"}
          </Button>
        </div>
      </form>
    </div>
  );
}
