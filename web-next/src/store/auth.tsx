import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, APIError } from "@/lib/api";
import type { UserStatus } from "@/lib/types";

interface AuthState {
  isAuthenticated: boolean;
  username: string | null;
  mustChangePassword: boolean;
  // isBootstrapping 标记启动探活是否仍在进行。刷新/首次打开时，在 /user/status
  // 返回前绝不能把界面判成「未登录」并跳 /login，否则会把当前 URL 吞掉、探活
  // 成功后又被 /login 路由重定向到 /dashboard（表现为「刷新即重新登录并跳主页」）。
  isBootstrapping: boolean;
}

interface AuthContextValue extends AuthState {
  login: (username: string, password: string, expire: number) => Promise<void>;
  logout: () => Promise<void>;
  refreshStatus: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

/**
 * 清除所有 `novaveil:chat:*` localStorage 键，防止跨用户泄漏对话历史与 mask 会话 ID。
 *
 * logout 时主动调用；login 成功后也兜底调用一次（token 过期、浏览器关闭等未走
 * logout 的场景）。倒序遍历避免 removeItem 导致的索引偏移。
 */
function clearChatLocalStorage(): void {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const key = localStorage.key(i);
      if (key && key.startsWith("novaveil:chat:")) {
        localStorage.removeItem(key);
      }
    }
  } catch {
    // localStorage 不可用（隐私模式/SSR）时静默忽略
  }
}

/**
 * 认证上下文：靠 JWT cookie 维持登录态
 *  - 启动时调 /user/status 探活；200 即已登录
 *  - login() 调 /user/login，后端 Set-Cookie；前端不存 token
 *  - 必须改密标志来自 /user/status 响应
 *  - logout() 取消在途查询并清空全部 React Query 缓存，防止下一个会话复用
 *    上一个用户的敏感数据（渠道明文 Key、API Key 明文等）。
 *  - generation 计数器：login/logout/refreshStatus 递增，异步结果返回时若
 *    generation 已过期则丢弃，防止旧请求覆盖新登录态（竞态守卫）。
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    isAuthenticated: false,
    username: null,
    mustChangePassword: false,
    isBootstrapping: true,
  });
  const queryClient = useQueryClient();
  // 每次发起会改变登录态的异步操作时递增；结果返回时若不匹配则丢弃。
  const generationRef = useRef(0);

  // 启动探活
  useEffect(() => {
    let cancelled = false;
    const gen = generationRef.current;
    (async () => {
      try {
        // 信封 data 缺失/畸形时兜底为空对象，探活失败不该炸掉整棵组件树
        const s = (await api.status()) ?? ({} as UserStatus);
        if (cancelled || gen !== generationRef.current) return;
        setState((prev) => ({
          ...prev,
          isBootstrapping: false,
          isAuthenticated: true,
          // 旧后端可能不带 username: 空串回落 null, Topbar 走自己的兜底而不是空名
          username: s.username || prev.username,
          mustChangePassword: s.must_change_password ?? false,
        }));
      } catch {
        // 探活结束：无论何种失败都退出 bootstrapping，让 UI 切换到登录页（仅
        // 401 明确视为未登录；网络/CORS 错误下 isAuthenticated 本就为 false）。
        if (cancelled || gen !== generationRef.current) return;
        setState((prev) => ({
          ...prev,
          isBootstrapping: false,
          isAuthenticated: false,
        }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback<AuthContextValue["login"]>(
    async (username, password, expire = 0) => {
      const gen = ++generationRef.current;
      const s = await api.login({ username, password, expire });
      if (gen !== generationRef.current) return;
      // 新会话建立后清除上一个用户残留的对话 localStorage（兜底：token 过期、
      // 浏览器关闭等未走 logout 的场景），避免跨用户泄漏对话历史与 mask 会话 ID。
      clearChatLocalStorage();
      setState({
        isAuthenticated: true,
        username,
        mustChangePassword: s.must_change_password,
        isBootstrapping: false,
      });
    },
    [],
  );

  const logout = useCallback(async () => {
    const gen = ++generationRef.current;
    // 1. 通知后端清除 JWT cookie（best-effort：后端不可达时前端仍清状态）。
    try {
      await api.logout();
    } catch {
      /* 即使后端失败，前端也清状态 */
    }
    if (gen !== generationRef.current) return;
    // 2. 取消在途查询，避免迟到的响应把上一个用户的敏感数据写回缓存。
    try {
      await queryClient.cancelQueries();
    } catch {
      /* cancelQueries 不应 reject，兜底 */
    }
    // 3. 清空全部查询缓存（渠道明文 Key、API Key 明文等不复用）。
    queryClient.clear();
    // 4. 清除对话相关 localStorage（对话历史、mask 会话 ID），防止下一个用户
    //    看到上一个用户的对话或复用其 X-Session-Id。
    clearChatLocalStorage();
    // 5. 更新认证状态。
    setState({
      isAuthenticated: false,
      username: null,
      mustChangePassword: false,
      isBootstrapping: false,
    });
  }, [queryClient]);

  const refreshStatus = useCallback(async () => {
    const gen = ++generationRef.current;
    try {
      const s = (await api.status()) ?? ({} as UserStatus);
      if (gen !== generationRef.current) return;
      // 同步回填 username: 改名后无需重新登录即可刷新 Topbar 显示
      setState((prev) => ({
        ...prev,
        username: s.username || prev.username,
        mustChangePassword: s.must_change_password ?? false,
      }));
    } catch (err) {
      if (gen !== generationRef.current) return;
      // JWT 过期或后端不可达: 401 让本地退到未登录态, 其他错误吞掉,
      // 避免业务页面因一次后台刷新崩溃。
      if (err instanceof APIError && err.status === 401) {
        setState({
          isAuthenticated: false,
          username: null,
          mustChangePassword: false,
          isBootstrapping: false,
        });
      }
    }
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({ ...state, login, logout, refreshStatus }),
    [state, login, logout, refreshStatus],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth 必须在 AuthProvider 内使用");
  return ctx;
}
