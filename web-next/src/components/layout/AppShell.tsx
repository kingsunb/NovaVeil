import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { Menu } from "lucide-react";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import { CommandPalette } from "./CommandPalette";
import { Button } from "@/components/ui/button";

/**
 * 应用壳：磨砂玻璃控制台布局。
 * 桌面使用常驻导航，小屏切换为可关闭的抽屉，保证主区始终有完整阅读宽度。
 */
const MOBILE_DRAWER_QUERY = "(max-width: 767px)";
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function prefersMobileDrawer(): boolean {
  try {
    if (typeof window.matchMedia !== "function") return false;
    return window.matchMedia(MOBILE_DRAWER_QUERY).matches;
  } catch {
    return false;
  }
}

export function AppShell({ children }: { children: ReactNode }) {
  const [cmdkOpen, setCmdkOpen] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [drawerMode, setDrawerMode] = useState(prefersMobileDrawer);
  const { pathname } = useLocation();
  const mainRef = useRef<HTMLElement>(null);
  const drawerRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const trap = mobileNavOpen && drawerMode;

  // 在新路由绘制前即时复位主区；查询参数、hash 和壳层状态更新不影响滚动。
  useLayoutEffect(() => {
    if (mainRef.current) mainRef.current.scrollTop = 0;
  }, [pathname]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setCmdkOpen((o) => !o);
      }
      if (e.key === "Escape") setMobileNavOpen(false);
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    document.body.style.overflow = mobileNavOpen ? "hidden" : "";
    return () => {
      document.body.style.overflow = "";
    };
  }, [mobileNavOpen]);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    let mq: MediaQueryList;
    try {
      mq = window.matchMedia(MOBILE_DRAWER_QUERY);
    } catch {
      return;
    }
    const apply = () => setDrawerMode(mq.matches);
    apply();
    mq.addEventListener?.("change", apply);
    return () => mq.removeEventListener?.("change", apply);
  }, []);

  // 小屏抽屉：焦点圈在侧栏内，关闭后回到打开它的按钮。桌面常驻侧栏不圈定。
  useEffect(() => {
    if (!trap) return;
    const drawer = drawerRef.current;
    if (!drawer) return;
    const panel: HTMLElement = drawer;
    const opener = openerRef.current;

    const focusable = () =>
      Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => !el.hasAttribute("disabled") && el.tabIndex >= 0,
      );

    const items = focusable();
    (items[0] ?? panel).focus();

    function onKey(e: KeyboardEvent) {
      if (e.key !== "Tab") return;
      const list = focusable();
      if (list.length === 0) {
        e.preventDefault();
        panel.focus();
        return;
      }
      const first = list[0];
      const last = list[list.length - 1];
      const active = document.activeElement;
      const inside = active instanceof Node && panel.contains(active);
      if (e.shiftKey && (!inside || active === first)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (!inside || active === last)) {
        e.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      if (opener && document.contains(opener)) opener.focus();
    };
  }, [trap]);

  function openNavigation() {
    const active = document.activeElement;
    openerRef.current = active instanceof HTMLElement ? active : null;
    setMobileNavOpen(true);
  }

  return (
    <div className="bg-gradient-subtle flex h-full min-h-0 bg-background text-foreground">
      <div
        aria-hidden={!mobileNavOpen}
        className={mobileNavOpen ? "fixed inset-0 z-40 bg-ink/35 backdrop-blur-[2px] md:hidden" : "hidden"}
        onClick={() => setMobileNavOpen(false)}
      />
      <div
        ref={drawerRef}
        tabIndex={-1}
        role={trap ? "dialog" : undefined}
        aria-modal={trap ? true : undefined}
        aria-label={trap ? "主导航菜单" : undefined}
        className={
          mobileNavOpen
            ? "fixed inset-y-0 left-0 z-50 w-[min(84vw,18rem)] outline-none md:static md:w-auto"
            : "hidden md:block"
        }
      >
        <Sidebar onNavigate={() => setMobileNavOpen(false)} />
      </div>
      <div className="flex min-w-0 flex-1 flex-col" inert={trap ? true : undefined}>
        <Topbar
          onOpenCommand={() => setCmdkOpen(true)}
          onOpenNavigation={openNavigation}
        />
        <main ref={mainRef} className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6 sm:py-6 lg:px-8">
          <div className="mx-auto h-full w-full max-w-[1440px]">{children}</div>
        </main>
      </div>
      <CommandPalette open={cmdkOpen} onClose={() => setCmdkOpen(false)} />
    </div>
  );
}

export function MobileMenuButton({ onClick }: { onClick: () => void }) {
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label="打开主导航"
      title="打开主导航"
      onClick={onClick}
      className="h-11 w-11 rounded-control md:hidden"
    >
      <Menu className="h-4 w-4" aria-hidden />
    </Button>
  );
}
