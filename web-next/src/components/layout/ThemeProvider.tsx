import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

type Theme = "light" | "dark" | "system";
type ResolvedTheme = "light" | "dark";

interface ThemeContextValue {
  theme: Theme;
  resolved: ResolvedTheme;
  setTheme: (t: Theme) => void;
  toggle: () => void;
}

const STORAGE_KEY = "nv-theme";
const ThemeContext = createContext<ThemeContextValue | null>(null);

function resolve(theme: Theme, systemDark: boolean): ResolvedTheme {
  if (theme === "system") {
    return systemDark ? "dark" : "light";
  }
  return theme;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(() => {
    const t = localStorage.getItem(STORAGE_KEY);
    return (t as Theme) || "system";
  });
  // 系统主题变化必须进入 React state：之前只直接改 DOM dataset，context 里的
  // resolved 仍是旧值，Topbar 等消费 useTheme().resolved 的组件会永远停留在
  // 旧主题（审计 FE-05 / F-L4）。
  const [systemDark, setSystemDark] = useState<boolean>(() =>
    matchMedia("(prefers-color-scheme: dark)").matches,
  );

  const resolved = useMemo<ResolvedTheme>(
    () => resolve(theme, systemDark),
    [theme, systemDark],
  );

  useEffect(() => {
    document.documentElement.dataset.theme = resolved;
  }, [resolved]);

  useEffect(() => {
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const onChange = (e: MediaQueryListEvent) => setSystemDark(e.matches);
    // 监听器注册后校准一次，避免挂载与查询初始值之间的微小时差。
    setSystemDark(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  const setTheme = useCallback((t: Theme) => {
    localStorage.setItem(STORAGE_KEY, t);
    setThemeState(t);
  }, []);

  const toggle = useCallback(() => {
    const next = resolved === "dark" ? "light" : "dark";
    localStorage.setItem(STORAGE_KEY, next);
    setThemeState(next);
  }, [resolved]);

  const value = useMemo<ThemeContextValue>(
    () => ({ theme, resolved, setTheme, toggle }),
    [theme, resolved, setTheme, toggle],
  );

  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  );
}

export function useTheme() {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme 必须在 ThemeProvider 内使用");
  return ctx;
}
