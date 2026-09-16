import { Suspense, lazy, useEffect, useMemo, useRef, useState } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "@/store/auth";

import { AppShell } from "@/components/layout/AppShell";
import { ErrorBoundary } from "@/components/layout/ErrorBoundary";
import { Button } from "@/components/ui/button";
import {
  CardGridSkeleton,
  LoginSkeleton,
  LogsSkeleton,
  PageSkeleton,
  SettingsSkeleton,
  Skeleton,
  TableSkeleton,
} from "@/components/ui/skeleton";
import { ForceChangePassword } from "@/components/auth/ForceChangePassword";
import { loadFlags, shouldUseNewWeb, type Flags } from "@/lib/flags";
import {
  loadChannelsPage,
  loadChatPage,
  loadCustomModelsPage,
  loadDashboardPage,
  loadGroupsPage,
  loadKeysPage,
  loadLoginPage,
  loadLogsPage,
  loadMaskPage,
  loadModelEvalPage,
  loadSettingsPage,
} from "@/lib/route-loaders";
import { apiForbiddenEvent, apiUnauthorizedEvent } from "@/lib/api";
import { useBuildVersionCheck } from "@/lib/version-check";
import { clearStaleChunkReloadFlag } from "@/lib/app-recovery";

/**
 * 路由级代码分割 —— 每个 page 是独立 chunk
 *  首屏只加载 AppShell + Login，其他页面按需加载
 *  预期效果：初始 JS bundle 减 40-60%
 */
const LoginPage = lazy(loadLoginPage);
const DashboardPage = lazy(loadDashboardPage);
const ChannelsPage = lazy(loadChannelsPage);
const CustomModelsPage = lazy(loadCustomModelsPage);
const GroupsPage = lazy(loadGroupsPage);
const ModelEvalPage = lazy(loadModelEvalPage);
const MaskPage = lazy(loadMaskPage);
const KeysPage = lazy(loadKeysPage);
const LogsPage = lazy(loadLogsPage);
const SettingsPage = lazy(loadSettingsPage);
const ChatPage = lazy(loadChatPage);

/**
 * 懒加载 fallback —— 与最终页面同形状的骨架屏。
 * `fallback` 由各路由按目标页面结构传入，避免切换时先显示错误形状再整页跳变（§3.1）。
 * 未传时退回 Dashboard 形状的 PageSkeleton，保持向后兼容。
 */
function LazyPage({ children, fallback }: { children: React.ReactNode; fallback: React.ReactNode }) {
  return (
    <ErrorBoundary>
      <Suspense fallback={fallback}>{children}</Suspense>
    </ErrorBoundary>
  );
}

/**
 * 路由表 —— 与 DESIGN.md §2 信息架构完全对齐
 *  运营 Operations: dashboard / channels / groups
 *  接入 Access    : keys / logs / settings
 *
 * P3 灰度：在挂载时拉 /__flags/，按 new-web + rollout-percent + 用户 bucket
 * 决定走新前端还是跳回旧 web 入口。flags 拉取失败时降级到默认值（不阻塞）。
 */
/**
 * flags 未就绪时的回退值（模块级常量保证引用稳定，useMemo 的依赖比较才有意义）。
 * 与 lib/flags.ts 的 DEFAULT_FLAGS 保持一致。
 */
const FALLBACK_FLAGS: Flags = {
  "new-web": true,
  "rollout-percent": 100,
  "sticky-bucket": true,
  "ab-mode": "auto",
  "legacy-path": "/legacy",
};

export default function App() {
  const { isAuthenticated, username, logout, mustChangePassword, refreshStatus, isBootstrapping } =
    useAuth();
  const [flags, setFlags] = useState<Flags | null>(null);

  useEffect(() => {
    let alive = true;
    loadFlags().then((f) => {
      if (alive) setFlags(f);
    });
    return () => {
      alive = false;
    };
  }, []);

  // 前端版本看门狗：登录后轮询 /update/build-info 比对前后端构建指纹，
  // 服务端更新而页面未刷新时弹出「立即刷新」提示（详见 lib/version-check）。
  useBuildVersionCheck(isAuthenticated);

  // 页面稳定运行 15s 后解除「本次会话已因过期 chunk 自动刷新」标记
  // （lib/app-recovery），恢复下一次服务端更新时的自动刷新资格。
  useEffect(() => {
    const timer = window.setTimeout(clearStaleChunkReloadFlag, 15_000);
    return () => window.clearTimeout(timer);
  }, []);

  // 全局 401 监听：任何 API 返回 401（JWT 过期/被吊销）都统一登出跳登录。
  // ref 防 listener 闭包拿到过期的 isAuthenticated。
  const authedRef = useRef(isAuthenticated);
  authedRef.current = isAuthenticated;
  useEffect(() => {
    const onUnauthorized = () => {
      if (!authedRef.current) return;
      authedRef.current = false;
      void logout();
    };
    window.addEventListener(apiUnauthorizedEvent, onUnauthorized);
    return () => window.removeEventListener(apiUnauthorizedEvent, onUnauthorized);
  }, [logout]);

  // 全局「必须改密」403 监听：客户端标志与后端不一致时（如会话中途被
  // 重置为初始密码），用限频 5s 的 refreshStatus() 把标志同步回来，
  // 下面的强制改密门随之接管整个界面。
  const lastForbiddenSyncRef = useRef(0);
  useEffect(() => {
    const onForbidden = () => {
      const now = Date.now();
      if (now - lastForbiddenSyncRef.current >= 5000) {
        lastForbiddenSyncRef.current = now;
        void refreshStatus();
      }
    };
    window.addEventListener(apiForbiddenEvent, onForbidden);
    return () => window.removeEventListener(apiForbiddenEvent, onForbidden);
  }, [refreshStatus]);

  // flags 拉取中：先用默认（"新前端启用"），不阻塞首屏
  const effective: Flags = flags ?? FALLBACK_FLAGS;

  // 未启用新前端 OR 用户被分配到老桶：渲染回退页。
  // sticky 模式由 flags 复用用户桶或匿名会话桶；非 sticky 模式仍按次随机。
  // memo 避免无关 re-render 重复计算，保留非 sticky 模式的现有防抖动行为。
  const useNew = useMemo(
    () => shouldUseNewWeb(effective, username),
    [effective, username],
  );

  // 启动探活尚未完成：渲染加载骨架，绝不进入未登录分支。否则刷新时会在
  // /user/status 返回前被判成未登录，<Navigate to="/login"> 把当前 URL 吞掉，
  // 探活成功后又从 /login 重定向到 /dashboard（「刷新即重新登录并跳主页」）。
  if (isBootstrapping) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <div className="flex flex-col items-center gap-3">
          <Skeleton className="h-9 w-9 rounded-[10px]" />
          <Skeleton className="h-3 w-28" />
        </div>
      </div>
    );
  }

  if (!useNew) {
    return <RollbackNotice flags={effective} />;
  }

  if (!isAuthenticated) {
    return (
      <Suspense fallback={<LoginSkeleton />}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="*" element={<Navigate to="/login" replace />} />
        </Routes>
      </Suspense>
    );
  }

  // 首登强制改密：改密前整个应用只剩改密页，其余路由全部不可达。
  if (mustChangePassword) {
    return <ForceChangePassword />;
  }

  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/login" element={<Navigate to="/dashboard" replace />} />
        <Route
          path="/dashboard"
          element={
            <LazyPage fallback={<PageSkeleton />}>
              <DashboardPage />
            </LazyPage>
          }
        />
        <Route
          path="/channels"
          element={
            <LazyPage fallback={<TableSkeleton />}>
              <ChannelsPage />
            </LazyPage>
          }
        />
        <Route
          path="/custom-models"
          element={
            <LazyPage fallback={<TableSkeleton />}>
              <CustomModelsPage />
            </LazyPage>
          }
        />
        <Route
          path="/groups"
          element={
            <LazyPage fallback={<CardGridSkeleton />}>
              <GroupsPage />
            </LazyPage>
          }
        />
        <Route
          path="/model-eval"
          element={
            <LazyPage fallback={<TableSkeleton />}>
              <ModelEvalPage />
            </LazyPage>
          }
        />
        <Route
          path="/mask"
          element={
            <LazyPage fallback={<TableSkeleton />}>
              <MaskPage />
            </LazyPage>
          }
        />
        <Route
          path="/keys"
          element={
            <LazyPage fallback={<TableSkeleton />}>
              <KeysPage />
            </LazyPage>
          }
        />
        <Route
          path="/logs"
          element={
            <LazyPage fallback={<LogsSkeleton />}>
              <LogsPage />
            </LazyPage>
          }
        />
        <Route
          path="/settings"
          element={
            <LazyPage fallback={<SettingsSkeleton />}>
              <SettingsPage />
            </LazyPage>
          }
        />
        <Route
          path="/chat"
          element={
            <LazyPage fallback={<PageSkeleton />}>
              <ChatPage />
            </LazyPage>
          }
        />
        <Route path="*" element={<Navigate to="/dashboard" replace />} />
      </Routes>
    </AppShell>
  );
}

/**
 * 灰度回退页 —— 用户被分到旧桶时的兜底
 */
function RollbackNotice({ flags }: { flags: Flags }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="glass-panel glass-inset-highlight w-full max-w-md rounded-card p-6">
        <div className="mb-3 flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-[10px] bg-gradient-to-br from-[#007AFF] to-[#5856D6] text-sm font-bold text-white">
            N
          </div>
          <h1 className="text-base font-semibold tracking-tight text-ink">
            NovaVeil
          </h1>
        </div>
        <h2 className="text-sm font-medium text-ink">
          您当前使用经典版控制台
        </h2>
        <p className="mt-1 text-xs leading-relaxed text-ink-muted">
          为保证灰度期间体验稳定，您被分到了旧版控制台。如需尝试新版，请使用邀请链接或联系管理员调整灰度比例。
        </p>
        <div className="mt-4 flex gap-2">
          <a href={flags["legacy-path"]}>
            <Button variant="primary" size="sm">
              进入旧版控制台
            </Button>
          </a>
          <a href={window.location.href}>
            <Button variant="ghost" size="sm">
              重新尝试
            </Button>
          </a>
        </div>
        <p className="mt-3 text-[10px] text-ink-subtle">
          灰度开关：new-web={String(flags["new-web"])} · rollout={flags["rollout-percent"]}% · sticky={String(flags["sticky-bucket"])}
        </p>
      </div>
    </div>
  );
}
