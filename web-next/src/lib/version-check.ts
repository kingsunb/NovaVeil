import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";

/**
 * 前端版本看门狗：判定浏览器里运行的前端构建与后端二进制是否已错位。
 *
 * 背景：Go 二进制通过 go:embed 内嵌前端，镜像更新后服务端已是新前端，
 * 但浏览器里长开的管理页仍运行旧 bundle（无任何机制提醒它）。错位的表现：
 *  - 旧页面对新后端调 API，字段对不上导致功能异常；
 *  - 懒加载路由去取已被新构建删除的 chunk，直接报错（见 lib/app-recovery）。
 *
 * 方案（版本一致性检测，去掉了 SW 层 —— 本项目无 SW，
 * index.html 由后端下发 no-cache，普通 reload 即可拿到新前端）：
 *  - build.sh 把 VERSION/COMMIT 经 VITE_APP_* 注入前端（vite define）；
 *  - 后端提供轻量 /update/build-info（只读 ldflags 常量，无统计聚合）；
 *  - 本模块挂载 + 定时 + 窗口聚焦时轮询比对，错位则弹出常驻 toast
 *    提供立即刷新；刷新动作由用户确认，不在后台打断表单编辑。
 */

/** 编译期注入的前端构建版本 / 提交（vite-env.d.ts 声明；dev 下为空串）。 */
const FRONTEND_VERSION: string =
  typeof __APP_VERSION__ === "string" ? __APP_VERSION__ : "";
const FRONTEND_COMMIT: string =
  typeof __APP_COMMIT__ === "string" ? __APP_COMMIT__ : "";

export type BuildMismatch = "match" | "mismatch" | "unknown";

/** isJudgable 值可参与比较：非空且不是 dev/unknown 这类占位符。 */
function isJudgable(value: string): boolean {
  const v = value.trim().toLowerCase();
  return v !== "" && v !== "dev" && v !== "unknown";
}

/**
 * compareBuild 判定前端构建与后端二进制是否同源。
 *  - 两端 commit 都可判定：以 commit 为准（docker 流 version 恒为 dev-<sha>
 *    的 `dev` 前缀，版本号区分不了不同构建，commit 才是唯一指纹）；
 *  - 否则回退版本号：任一侧不可判定 → unknown（静默）；都可判定且不同 → mismatch。
 */
export function compareBuild(
  frontendCommit: string,
  backendCommit: string,
  frontendVersion: string,
  backendVersion: string,
): BuildMismatch {
  if (isJudgable(frontendCommit) && isJudgable(backendCommit)) {
    return frontendCommit.trim().toLowerCase() === backendCommit.trim().toLowerCase()
      ? "match"
      : "mismatch";
  }
  if (!isJudgable(frontendVersion) || !isJudgable(backendVersion)) {
    return "unknown";
  }
  const normalize = (v: string) => {
    const t = v.trim();
    return t.startsWith("v") || t.startsWith("V") ? t.slice(1) : t;
  };
  return normalize(frontendVersion) === normalize(backendVersion)
    ? "match"
    : "mismatch";
}

/** 轮询间隔：常驻页面的兜底节奏，主要依赖聚焦/切页触发。 */
const CHECK_INTERVAL_MS = 5 * 60 * 1000;
/** 聚焦触发节流：快速 alt-tab 时不打爆后端。 */
const FOCUS_THROTTLE_MS = 60 * 1000;

/**
 * useBuildVersionCheck 挂载后立即比对一次，之后每 5 分钟 + 窗口聚焦 /
 * 切回标签页时（节流 60s）再比对。判定为 mismatch 时弹常驻 toast（固定 id
 * 防止重复堆叠），由用户点击「立即刷新」整页加载新前端。
 */
export function useBuildVersionCheck(enabled: boolean): void {
  const lastFocusCheckRef = useRef(0);

  useEffect(() => {
    if (!enabled) return;

    let disposed = false;
    const timer: { id: number | undefined } = { id: undefined };

    const check = async () => {
      const info = await api.getBuildInfo().catch(() => null);
      if (disposed || !info) return;
      // 后端统计端点偶发 5xx 时 getBuildInfo 也可能暂时失败，静默等待下一轮。
      if (
        compareBuild(FRONTEND_COMMIT, info.commit, FRONTEND_VERSION, info.version) !==
        "mismatch"
      ) {
        return;
      }
      // 已错位：停掉轮询（toast 常驻，刷新前无需再探测），弹一次提示。
      disposed = true;
      if (timer.id !== undefined) window.clearInterval(timer.id);
      toast.info("检测到服务端已更新", {
        id: "novaveil-build-update",
        description:
          "当前页面仍在运行旧版本前端，部分功能可能异常。点击「立即刷新」加载新版本。",
        duration: Infinity,
        action: { label: "立即刷新", onClick: () => window.location.reload() },
      });
    };

    const checkThrottled = () => {
      const now = Date.now();
      if (now - lastFocusCheckRef.current < FOCUS_THROTTLE_MS) return;
      lastFocusCheckRef.current = now;
      void check();
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") checkThrottled();
    };

    void check();
    timer.id = window.setInterval(() => void check(), CHECK_INTERVAL_MS);
    window.addEventListener("focus", checkThrottled);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disposed = true;
      if (timer.id !== undefined) window.clearInterval(timer.id);
      window.removeEventListener("focus", checkThrottled);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [enabled]);
}
