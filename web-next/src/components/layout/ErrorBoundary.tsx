import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  isDynamicChunkError,
  recoverFromStaleBuild,
} from "@/lib/app-recovery";
interface Props {
  children: ReactNode;
  /** 自定义 fallback；不传则用默认兜底 */
  fallback?: (err: Error, reset: () => void) => ReactNode;
  /** 自定义错误上报（可对接 Sentry/PostHog） */
  onError?: (err: Error, info: ErrorInfo) => void;
}

interface State {
  err: Error | null;
  /** React 给的 componentStack，方便定位是哪个组件抛错 */
  componentStack: string | null;
  /**
   * 过期 chunk 失败且本次会话已自动刷新过（未成功恢复）：不再循环刷新，
   * 由默认 fallback 展示「版本已更新」文案 + 手动刷新按钮。
   */
  chunkFailure: boolean;
}

/**
 * 全局 ErrorBoundary
 *  - 捕获子组件渲染时的异常，UI 不白屏
 *  - 默认 fallback：可读的错误卡片 + 「重试」「整页刷新」两个动作
 *  - 重置通过 key remount，不破坏父组件状态
 *
 * 局限：只捕获 render / 生命周期 / 构造函数错误，不捕获：
 *  - 异步代码（setTimeout / Promise）
 *  - 事件处理器（用 try/catch）
 *  - SSR 阶段
 *  - 自身抛错
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { err: null, componentStack: null, chunkFailure: false };

  static getDerivedStateFromError(err: Error): Partial<State> {
    return { err, componentStack: null };
  }

  componentDidCatch(err: Error, info: ErrorInfo) {
    // 预留：上报到 Sentry
    this.props.onError?.(err, info);
    // 浏览器控制台保留
    console.error("[ErrorBoundary]", err, info.componentStack);
    // 同步存到 state，fallback UI 能直接渲染（便于调试）
    this.setState({ componentStack: info.componentStack ?? null });
    // 服务端更新后旧 bundle 引用的 chunk 已被删除，应用在此状态下不可恢复，
    // 直接整页刷新拉新前端（路由懒加载失败发生在页面切换时，不打断表单编辑）。
    // recoverFromStaleBuild 带会话级一次性保护；返回 false 表示已刷新过仍失败，
    // 置 chunkFailure 让 fallback 展示版本更新文案，由用户手动刷新。
    if (isDynamicChunkError(err) && !recoverFromStaleBuild()) {
      this.setState({ chunkFailure: true });
    }
  }

  reset = () => {
    this.setState({ err: null, componentStack: null, chunkFailure: false });
  };

  render() {
    const { err, componentStack, chunkFailure } = this.state;
    if (!err) return this.props.children;
    const showDiagnostics = import.meta.env.DEV;

    if (this.props.fallback) {
      return this.props.fallback(err, this.reset);
    }

    if (chunkFailure) {
      return (
        <div
          role="alert"
          className="flex min-h-[60vh] items-center justify-center px-6"
        >
          <div className="w-full max-w-md rounded-card border border-border bg-card p-6 shadow-overlay">
            <div className="mb-3 flex items-center gap-2">
              <span
                aria-hidden
                className="flex h-7 w-7 items-center justify-center rounded-full bg-primary/15 text-primary-text"
              >
                <RefreshCw className="h-4 w-4" />
              </span>
              <h2 className="text-base font-semibold text-ink">
                版本已更新
              </h2>
            </div>
            <p className="text-sm text-ink-muted">
              服务端已发布新版本，当前页面加载的组件已过期。请刷新页面获取最新前端。
            </p>
            <div className="mt-4 flex gap-2">
              <Button
                variant="primary"
                size="sm"
                onClick={() => window.location.reload()}
              >
                刷新页面
              </Button>
            </div>
          </div>
        </div>
      );
    }

    return (
      <div
        role="alert"
        className="flex min-h-[60vh] items-center justify-center px-6"
      >
        <div className="w-full max-w-md rounded-card border border-destructive/30 bg-destructive/5 p-6 shadow-overlay">
          <div className="mb-3 flex items-center gap-2">
            <span
              aria-hidden
              className="flex h-7 w-7 items-center justify-center rounded-full bg-destructive/15 text-destructive"
            >
              <AlertTriangle className="h-4 w-4" />
            </span>
            <h2 className="text-base font-semibold text-destructive">
              这块内容出错了
            </h2>
          </div>
          <p className="text-sm text-ink-muted">
            页面遇到意外错误，不会影响其他功能。可重试或整页刷新。
          </p>
          {showDiagnostics && (
            <>
              <pre className="mt-3 max-h-32 overflow-auto rounded-md bg-destructive/5 p-2 font-mono text-[11px] text-destructive/80">
                {err.message}
              </pre>
              {componentStack && (
                <details className="mt-2">
                  <summary className="cursor-pointer text-[11px] text-ink-muted hover:text-ink">
                    组件栈（点击展开定位）
                  </summary>
                  <pre className="mt-1 max-h-48 overflow-auto rounded-md bg-surface-subtle/60 p-2 font-mono text-[10px] text-ink-muted">
                    {componentStack.trim()}
                  </pre>
                </details>
              )}
            </>
          )}
          <div className="mt-4 flex gap-2">
            <Button variant="primary" size="sm" onClick={this.reset}>
              重试
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => window.location.reload()}
            >
              整页刷新
            </Button>
          </div>
        </div>
      </div>
    );
  }
}
