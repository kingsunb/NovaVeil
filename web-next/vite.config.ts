import { defineConfig, type PluginOption } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { visualizer } from "rollup-plugin-visualizer";
import path from "node:path";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

/**
 * web-next/ Vite 配置
 *  - build.outDir = ../static/out（与 web/ 旧配置一致；供 Go embed 抓取）
 *  - 旧 web/ 已退役（v0.2.0 P5）；新 web-next 接管前端构建
 *  - 文本类资源 closeBundle 阶段同步产出 .gz 预压缩文件, 后端静态中间件按
 *    Accept-Encoding 优先直发(零运行时压缩 CPU); 未预压缩的文本资源由后端
 *    动态 gzip 兜底, 不影响正常交付。
 *  - VISUALIZE=1 触发 bundle 报告（写到 ../static/out/stats.html）
 */
const buildOutDir = path.resolve(import.meta.dirname, "../static/out");

// novaveilGzipPrecompressPlugin 为每个文本类构建产物额外产出同名 .gz。
// 后端中间件优先直发 .gz(API 不变, 仅在第一次请求时省 CPU); 未预压缩(如本地
// 目录模式或新增 ext)回退到后端动态压缩, 行为与改造前等价。
function novaveilGzipPrecompressPlugin(): PluginOption {
  const compressible = /\.(?:js|css|svg|html|json|map|txt|webmanifest)$/;
  return {
    name: "novaveil-gzip-precompress",
    apply: "build",
    closeBundle() {
      const walk = (relative = ""): string[] =>
        readdirSync(path.join(buildOutDir, relative)).flatMap((name) => {
          const child = path.join(relative, name);
          return statSync(path.join(buildOutDir, child)).isDirectory()
            ? walk(child)
            : [child];
        });
      let emitted = 0;
      for (const relativePath of walk()) {
        if (!compressible.test(relativePath) || relativePath.endsWith(".gz")) continue;
        const filePath = path.join(buildOutDir, relativePath);
        writeFileSync(`${filePath}.gz`, gzipSync(readFileSync(filePath), { level: 9 }));
        emitted += 1;
      }
      if (emitted === 0) {
        throw new Error("gzip precompress produced no files");
      }
    },
  };
}

export default defineConfig({
  plugins: [tailwindcss(), react(), novaveilGzipPrecompressPlugin()],
  define: {
    // 构建版本注入（来源：scripts/build.sh 传入的 VITE_APP_* 环境变量），
    // 编译为全局常量 __APP_VERSION__ / __APP_COMMIT__，供版本看门狗比较。
    // 本地 dev / 未设置时为空串，看门狗按「不可判定」静默处理。
    __APP_VERSION__: JSON.stringify(process.env.VITE_APP_VERSION ?? ""),
    __APP_COMMIT__: JSON.stringify(process.env.VITE_APP_COMMIT ?? ""),
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  build: {
    outDir: path.resolve(import.meta.dirname, "../static/out"),
    emptyOutDir: true,
    rollupOptions: {
      plugins: process.env.VISUALIZE
        ? [visualizer({ filename: "stats.html", gzipSize: true, brotliSize: true })]
        : [],
    },
  },
  server: {
    port: 5174,
    host: "0.0.0.0",
    proxy: {
      // 开发态把 /api 代理到后端，避免 CORS。
      // changeOrigin 必须为 false：后端 OriginProtection 中间件会比较
      // 请求 Origin 头与 req.Host 来防 CSRF；若 vite 改写 Host 头为 127.0.0.1:8080，
      // 浏览器从外网 IP 访问时 origin 与 host 对不上，会被后端返回 403 Forbidden。
      // 关掉 changeOrigin 后 vite 透传浏览器原始 Host，后端就能对得上放行。
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      // 对话页直接调用 /v1/chat/completions 走完整 relay 管线, 开发态同样需要代理到后端。
      "/v1": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
