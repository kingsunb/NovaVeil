import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";

// vitest.config.ts 的 define 注入 __APP_VERSION__="test-version" /
// __APP_COMMIT__="test-commit"，hook 的比较基线由此确定。
vi.mock("@/lib/api", () => ({ api: { getBuildInfo: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { info: vi.fn() } }));

import { api } from "@/lib/api";
import { toast } from "sonner";
import { compareBuild, useBuildVersionCheck } from "./version-check";
import {
  clearStaleChunkReloadFlag,
  isDynamicChunkError,
  shouldAutoReloadStaleChunk,
} from "./app-recovery";

const getBuildInfoMock = vi.mocked(api.getBuildInfo);
const toastInfoMock = vi.mocked(toast.info);

describe("compareBuild", () => {
  it("两端 commit 可判定时以 commit 为准（docker dev-<sha> 流）", () => {
    // 服务端更新后 commit 错位 → mismatch
    expect(compareBuild("bb04581", "c0ffee1", "dev-bb04581", "dev-c0ffee1")).toBe(
      "mismatch",
    );
    // 同一构建（大小写不敏感）→ match
    expect(compareBuild("BB04581", "bb04581", "dev-x", "dev-x")).toBe("match");
  });

  it("commit 缺失时回退版本号比较", () => {
    // 版本一致（含 v 前缀归一化）
    expect(compareBuild("", "unknown", "v0.1.0", "0.1.0")).toBe("match");
    // 版本不同
    expect(compareBuild("", "", "v0.0.0", "v0.1.0")).toBe("mismatch");
  });

  it("任一侧不可判定（空/dev/unknown）时返回 unknown，不告警", () => {
    // 本地 dev：前端常量为空串，后端 go run 无 ldflags
    expect(compareBuild("", "", "", "dev")).toBe("unknown");
    expect(compareBuild("dev", "dev", "dev", "dev")).toBe("unknown");
    expect(compareBuild("", "unknown", "dev", "unknown")).toBe("unknown");
    // 后端可判定、前端不可判定 → unknown
    expect(compareBuild("", "bb04581", "", "v0.1.0")).toBe("unknown");
  });

  it("dev-<sha> 版本号可判定（与纯 dev 占位符区分）", () => {
    expect(compareBuild("", "", "dev-bb04581", "dev-bb04581")).toBe("match");
    expect(compareBuild("", "", "dev-bb04581", "dev-fffffff")).toBe("mismatch");
  });
});

describe("useBuildVersionCheck", () => {
  beforeEach(() => {
    getBuildInfoMock.mockReset();
    toastInfoMock.mockClear();
  });

  it("enabled=false 时不轮询", () => {
    renderHook(() => useBuildVersionCheck(false));
    expect(getBuildInfoMock).not.toHaveBeenCalled();
  });

  it("前后端构建一致时不弹提示", async () => {
    getBuildInfoMock.mockResolvedValue({
      version: "test-version",
      commit: "test-commit",
      build_time: "",
    });
    renderHook(() => useBuildVersionCheck(true));
    await waitFor(() => expect(getBuildInfoMock).toHaveBeenCalledTimes(1));
    expect(toastInfoMock).not.toHaveBeenCalled();
  });

  it("前后端构建错位时弹出常驻刷新提示", async () => {
    getBuildInfoMock.mockResolvedValue({
      version: "dev-fffffff",
      commit: "fffffff",
      build_time: "",
    });
    renderHook(() => useBuildVersionCheck(true));
    await waitFor(() => expect(toastInfoMock).toHaveBeenCalledTimes(1));
    expect(toastInfoMock).toHaveBeenCalledWith(
      "检测到服务端已更新",
      expect.objectContaining({
        id: "novaveil-build-update",
        duration: Infinity,
        action: expect.objectContaining({ label: "立即刷新" }),
      }),
    );
  });

  it("接口失败时静默等待下一轮", async () => {
    getBuildInfoMock.mockRejectedValue(new Error("network down"));
    renderHook(() => useBuildVersionCheck(true));
    await waitFor(() => expect(getBuildInfoMock).toHaveBeenCalled());
    expect(toastInfoMock).not.toHaveBeenCalled();
  });
});

describe("isDynamicChunkError", () => {
  it("识别 Vite / webpack 的动态模块加载失败", () => {
    expect(
      isDynamicChunkError(
        new Error(
          "Failed to fetch dynamically imported module: http://x/assets/Dashboard-abc.js",
        ),
      ),
    ).toBe(true);
    expect(
      isDynamicChunkError(new Error("Importing a module script failed.")),
    ).toBe(true);
    expect(isDynamicChunkError(new Error("Loading chunk 4 failed"))).toBe(true);
  });

  it("普通错误不误判", () => {
    expect(isDynamicChunkError(new Error("network down"))).toBe(false);
    expect(isDynamicChunkError(new Error("Unexpected token < in JSON"))).toBe(
      false,
    );
  });
});

describe("shouldAutoReloadStaleChunk", () => {
  it("同一次会话内只允许自动刷新一次（防循环）", () => {
    sessionStorage.clear();
    expect(shouldAutoReloadStaleChunk()).toBe(true);
    expect(shouldAutoReloadStaleChunk()).toBe(false);
    clearStaleChunkReloadFlag();
    expect(shouldAutoReloadStaleChunk()).toBe(true);
    sessionStorage.clear();
  });
});
