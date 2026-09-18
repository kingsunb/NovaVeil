/// <reference types="vitest" />
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  define: {
    // 与 vite.config.ts 的 define 对齐：vitest 不读 vite.config.ts，若不注入，
    // 模块里的 typeof 守卫会退化成空串，版本看门狗的 mismatch 分支测不到。
    __APP_VERSION__: JSON.stringify("test-version"),
    __APP_COMMIT__: JSON.stringify("test-commit"),
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  test: {
    globals: true,
    environment: "happy-dom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // 排除 E2E（Playwright 单独跑）
    exclude: [
      "**/node_modules/**",
      "**/dist/**",
      "**/.{idea,git,cache,output,temp}/**",
      "e2e/**",
    ],
    coverage: {
      provider: "v8",
      reporter: ["text", "html", "json-summary"],
      include: [
        "src/lib/**",
        "src/components/ui/**",
        "src/components/charts/**",
      ],
      exclude: ["**/*.test.{ts,tsx}", "**/*.d.ts"],
      // P4.1 内部验收目标：≥90%
      //  - 下降超过阈值会 CI 失败
      thresholds: {
        lines: 88,        // 给 2% buffer（新代码可能略低）
        functions: 80,
        branches: 85,
        statements: 88,
      },
    },
  },
});
