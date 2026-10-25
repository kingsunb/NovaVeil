# Bun 1.4.4 与官方 OpenCode CLI 同版本, 供免费档 sidecar 使用真 BoringSSL 发送。
# 从官方 Alpine 镜像复制二进制, 保留下方已固定的 Alpine 3.21.7 运行时。
FROM oven/bun:1.4.4-alpine@sha256:4cf82f5686b196504fe119db3fcd9185b191f82ece82c40680e597228ec6070a AS bun

# Pin the complete Alpine 3.21.7 multi-platform index so each TARGETPLATFORM selects
# its verified child manifest without allowing a mutable tag to replace the base.
FROM alpine:3.21.7@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

# 固定的容器运行身份。宿主 bind mount 必须预先归属这组数字 UID/GID。
ARG APP_UID=10001
ARG APP_GID=10001
ARG TARGETPLATFORM

ENV TZ=Asia/Shanghai \
    APP_NAME=novaveil \
    NO_COLOR=1 \
    NOVEIL_BUN_BIN=/usr/local/bin/bun

# 仅安装运行时必须的工具:
#   - ca-certificates: 访问 HTTPS 上游。
#   - tzdata:          配合 ENV TZ 提供时区数据库。
#   - libstdc++/libgcc: Bun 动态依赖。
# BusyBox 自带 wget, HEALTHCHECK 不额外安装 curl。
RUN apk add --no-cache ca-certificates tzdata libstdc++ libgcc \
 && rm -rf /var/cache/apk/* \
 && addgroup -g "${APP_GID}" -S novaveil \
 && adduser -u "${APP_UID}" -S -D -H -h /app -s /sbin/nologin -G novaveil novaveil \
 && mkdir -p /app/data \
 && chown "${APP_UID}:${APP_GID}" /app/data \
 && chmod 0700 /app/data

# 只 COPY 运行必需产物。应用、Bun 与许可证保持 root 所有、只读; 仅 /app/data
# 归属应用用户, 防止运行时修改二进制或供应链报告。权限在 COPY 时设置, 避免
# 后续 chmod 触发大文件在 overlay 层复制。
COPY --from=bun --chmod=0555 /usr/local/bin/bun /usr/local/bin/bun
COPY --chmod=0555 build/docker/${TARGETPLATFORM}/${APP_NAME} /app/${APP_NAME}
COPY --chmod=0444 LICENSE /app/LICENSE
COPY --chmod=0444 build/THIRD_PARTY_LICENSES.csv /app/THIRD_PARTY_LICENSES.csv
COPY --chmod=0444 build/THIRD_PARTY_LICENSES.frontend.json /app/THIRD_PARTY_LICENSES.frontend.json
COPY --chmod=0555 scripts/dockerfile/entrypoint.sh /entrypoint.sh

USER ${APP_UID}:${APP_GID}
WORKDIR /app
EXPOSE 8080

# 镜像 wget 探测数据库探活端点
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["start"]
