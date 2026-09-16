import { NavLink } from "react-router-dom";
import {
  Activity,
  Bot,
  ChevronsLeft,
  ChevronsRight,
  FlaskConical,
  GaugeCircle,
  KeyRound,
  LayoutGrid,
  MessageSquare,
  Settings as SettingsIcon,
  ShieldCheck,
  UsersRound,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { preloadPage } from "@/lib/page-loaders";
import { BrandMark } from "@/components/ui/brand-mark";
import { useSidebar } from "./useSidebar";

interface NavItem {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
}

const OPERATIONS: NavItem[] = [
  { to: "/dashboard", label: "总览", icon: GaugeCircle },
  { to: "/channels", label: "渠道", icon: LayoutGrid },
  { to: "/custom-models", label: "自定义模型", icon: Bot },
  { to: "/groups", label: "分组", icon: UsersRound },
  { to: "/model-eval", label: "模型评估", icon: FlaskConical },
  { to: "/mask", label: "脱敏", icon: ShieldCheck },
  { to: "/chat", label: "对话", icon: MessageSquare },
];

const ACCESS: NavItem[] = [
  { to: "/keys", label: "API 密钥", icon: KeyRound },
  { to: "/logs", label: "日志", icon: Activity },
  { to: "/settings", label: "设置", icon: SettingsIcon },
];

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { collapsed, toggle } = useSidebar();

  return (
    <aside
      id="primary-sidebar"
      data-testid="sidebar"
      data-collapsed={collapsed}
      aria-label="主导航"
      className={cn(
        "glass-sidebar group/sidebar relative z-30 flex h-full shrink-0 flex-col transition-[width] duration-200 ease-[cubic-bezier(0.25,0,0,1)]",
        collapsed ? "w-14" : "w-60",
      )}
    >
      {/* 品牌 */}
      <div className={cn("flex h-14 items-center px-3.5", collapsed && "justify-center px-0")}>
        <BrandMark size="sm" withName={!collapsed} />
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto px-2.5 py-2" aria-label="导航">
        <NavGroup label="运营" items={OPERATIONS} collapsed={collapsed} onNavigate={onNavigate} />
        <NavGroup label="接入" items={ACCESS} collapsed={collapsed} onNavigate={onNavigate} />
      </nav>

      {/* 折叠按钮 */}
      <div className="border-t border-border/40 p-3">
        <button
          type="button"
          onClick={toggle}
          aria-label={collapsed ? "展开侧栏" : "折叠侧栏"}
          aria-pressed={collapsed}
          aria-controls="primary-sidebar"
          data-testid="collapse-btn"
          className={cn(
            // 44px 高：WCAG 2.5.5 交互目标下限
            "flex h-11 w-full items-center gap-2.5 rounded-control px-2 text-[13px] transition-all duration-150",
            "text-ink-subtle hover:bg-ink/[0.04] hover:text-ink-muted",
            collapsed && "justify-center px-0",
          )}
        >
          {collapsed ? (
            <ChevronsRight className="h-4 w-4 shrink-0" aria-hidden />
          ) : (
            <ChevronsLeft className="h-4 w-4 shrink-0" aria-hidden />
          )}
          <span className={cn(collapsed && "hidden")}>折叠</span>
        </button>
      </div>
    </aside>
  );
}

function NavGroup({
  label,
  items,
  collapsed,
  onNavigate,
}: {
  label: string;
  items: NavItem[];
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  return (
    <div className="space-y-0.5">
      <p
        className={cn(
          "px-2 pb-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-ink-subtle",
          collapsed && "sr-only",
        )}
      >
        {label}
      </p>
      {items.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          title={collapsed ? item.label : undefined}
          // 折叠态下文字 span 被 hidden，accessible name 必须由 aria-label 兜底，
          // 否则读屏用户丢失整个主导航（审计 4.17）。
          aria-label={item.label}
          onClick={onNavigate}
          // 桌面 hover / 键盘 focus 时预加载目标 chunk（§3.3）。移动端无 hover，
          // 点击仍走 lazy() 的正常加载路径；preloadPage 走缓存 loader，在途/成功
          // 不重复 import，失败不留缓存不阻断导航。
          onMouseEnter={() => void preloadPage(item.to)}
          onFocus={() => void preloadPage(item.to)}
          className={({ isActive }) =>
            cn(
              "flex min-h-10 items-center gap-2.5 rounded-control px-2 text-[13px] font-medium tracking-tight transition-all duration-150",
              collapsed && "justify-center px-0",
              isActive
                ? "bg-primary/[0.10] text-primary-text"
                : "text-ink-muted hover:bg-ink/[0.04] hover:text-ink",
            )
          }
        >
          <item.icon className="h-4 w-4 shrink-0" aria-hidden />
          <span className={cn("truncate", collapsed && "hidden")}>
            {item.label}
          </span>
        </NavLink>
      ))}
    </div>
  );
}
