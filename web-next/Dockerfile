# =============================================================================
# NovaVeil web-next · 生产构建（多阶段）
#  - Stage 1: install + build（含 tsc 类型检查 + vite build）
#  - Stage 2: nginx alpine 提供 dist/，并反代 /api 到后端
#  - 官方 nginx entrypoint 渲染配置模板；master 以 root 启动，worker 降权 nginx
#  - read-only rootfs + no-new-privileges（见 docker-compose.yml web-next profile）
#
# 注意：默认部署方式是 Go 内嵌前端（见仓库根 scripts/build.sh + static/static.go），
#   本 Dockerfile 仅用于「独立前端部署」的可选场景（docker-compose --profile web-next）。
#
# 构建（build context = web-next/）：
#   docker build -t novaveil-web-next:<ver> web-next/
# 运行：
#   docker run --rm -p 8080:8080 \
#     -e NOVAVEIL_UPSTREAM=novaveil:8080 \
#     novaveil-web-next:<ver>
# =============================================================================

# ---- Stage 1: builder ----
FROM node:22-alpine AS builder
WORKDIR /app

# 利用层缓存：单独先装依赖。
# pnpm@9 与 pnpm-lock.yaml lockfileVersion 9.0 对齐（与 scripts/build.sh 一致）。
COPY package.json pnpm-lock.yaml* ./
RUN corepack enable && corepack prepare pnpm@9 --activate \
 && pnpm install --frozen-lockfile

# 复制源码 + 构建
COPY tsconfig*.json vite.config.ts postcss.config.js components.json ./
COPY index.html ./
COPY src ./src
COPY public ./public
RUN pnpm build
# Vite 输出到 ../static/out（相对 WORKDIR /app 即 /static/out），
# 供 Go embed 抓取；在容器内构建时产物落在 /static/out。

# ---- Stage 2: runtime (nginx alpine) ----
FROM nginx:1.27-alpine AS runtime

# 用 wget 替代 curl 做健康检查（更小的 alpine 基础）
RUN apk add --no-cache wget \
 && rm -f /etc/nginx/conf.d/default.conf

# 后端 API upstream 默认值；运行时可用 -e NOVAVEIL_UPSTREAM=host:port 覆盖。
# 官方 nginx 镜像内置 envsubst：/etc/nginx/templates/*.template 渲染到
# /etc/nginx/conf.d/，只替换实际存在的环境变量，nginx 内置变量（$host 等）不受影响。
ENV NOVAVEIL_UPSTREAM=novaveil:8080

# 拷贝构建产物（Vite 输出在 /static/out，不是 /app/dist）+ nginx 配置模板
COPY --from=builder /static/out /usr/share/nginx/html
COPY nginx.conf /etc/nginx/templates/default.conf.template

# 特性开关：构建期可注入默认 JSON（运行时由 /api/v1/__flags 覆盖）
ARG WEB_FLAGS_JSON="{}"
RUN mkdir -p /usr/share/nginx/html/__flags \
 && echo "$WEB_FLAGS_JSON" > /usr/share/nginx/html/__flags/default.json

# 暴露 8080（非 root-friendly，避免 80 端口的权限问题）
EXPOSE 8080

# 健康检查：wget 拉根路径，5s 超时
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

# 官方 ENTRYPOINT（/docker-entrypoint.sh）先渲染模板再 exec CMD；
# nginx master 以 root 启动，worker 自动降权到 nginx 用户。
CMD ["nginx", "-g", "daemon off;"]
