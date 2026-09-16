import { useLocation } from "react-router-dom";
import { Menu, Moon, Sun, LogOut, Search } from "lucide-react";
import { useTheme } from "./ThemeProvider";
import { useAuth } from "@/store/auth";
import { Button } from "@/components/ui/button";

const TITLES: Record<string, string> = {
  "/dashboard": "总览",
  "/channels": "渠道",
  "/custom-models": "自定义模型",
  "/groups": "分组",
  "/mask": "脱敏",
  "/chat": "对话",
  "/keys": "API 密钥",
  "/logs": "日志",
  "/settings": "设置",
};

interface TopbarProps {
  onOpenCommand: () => void;
  onOpenNavigation: () => void;
}

const PAGE_HINTS: Record<string, string> = {
  "/dashboard": "请求、用量与近期错误",
  "/channels": "上游渠道与模型目录",
  "/custom-models": "固定回复，不走上游",
  "/groups": "故障转移与手动选路",
  "/mask": "出网前自动脱敏",
  "/chat": "走完整中继管线试对话",
  "/keys": "客户端凭据与限流",
  "/logs": "实时请求与错误追踪",
  "/settings": "外观、账户与系统",
};

export function Topbar({ onOpenCommand, onOpenNavigation }: TopbarProps) {
  const { resolved, toggle } = useTheme();
  const { username, logout } = useAuth();
  const { pathname } = useLocation();
  const title = TITLES[pathname] ?? "NovaVeil";
  const hint = PAGE_HINTS[pathname];
  const initial = (username ?? "A")[0]?.toUpperCase() ?? "A";

  return (
    <header className="glass-topbar sticky top-0 z-20 flex h-14 items-center gap-2 px-3 sm:gap-3 sm:px-6">
      <Button
        variant="ghost"
        size="icon"
        aria-label="打开主导航"
        title="打开主导航"
        onClick={onOpenNavigation}
        className="h-11 w-11 rounded-control md:hidden"
      >
        <Menu className="h-4 w-4" aria-hidden />
      </Button>
      <div className="min-w-0">
        <h2 className="truncate text-[15px] font-semibold tracking-tight text-ink">{title}</h2>
        {hint && (
          <p className="hidden truncate text-[11px] text-ink-subtle sm:block">{hint}</p>
        )}
      </div>

      <div className="ml-auto flex shrink-0 items-center gap-1.5 sm:gap-2">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onOpenCommand}
          aria-label="打开命令面板"
          aria-keyshortcuts="Control+k Meta+k"
          aria-haspopup="dialog"
          title="打开命令面板（Ctrl/Cmd + K）"
          className="h-11 w-11 shrink-0 rounded-control lg:w-auto lg:px-3"
        >
          <Search className="h-4 w-4" aria-hidden />
          <span className="hidden lg:inline">命令面板</span>
          <kbd className="hidden rounded border border-border px-1 text-[10px] text-ink-subtle lg:inline">
            Ctrl/Cmd + K
          </kbd>
        </Button>
        <Button
          variant="ghost"
          size="icon"
          onClick={toggle}
          aria-label="切换主题"
          title="切换主题"
          className="h-9 w-9 rounded-full"
        >
          {resolved === "dark" ? (
            <Sun className="h-4 w-4" aria-hidden />
          ) : (
            <Moon className="h-4 w-4" aria-hidden />
          )}
        </Button>

        <div className="mx-1 hidden h-5 w-px bg-border/60 sm:block" aria-hidden />

        <Button
          variant="ghost"
          size="sm"
          onClick={logout}
          title="退出登录"
          className="gap-2 rounded-full px-1.5 sm:px-2"
        >
          <span className="flex h-6 w-6 items-center justify-center rounded-full bg-primary text-[11px] font-semibold text-primary-foreground">
            {initial}
          </span>
          <span className="hidden text-xs font-medium sm:inline">{username ?? "admin"}</span>
          <LogOut className="h-3.5 w-3.5 text-ink-subtle" aria-hidden />
        </Button>
      </div>
    </header>
  );
}
