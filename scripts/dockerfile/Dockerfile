# Pin the complete Alpine 3.21.7 multi-platform index so each TARGETPLATFORM selects
# its verified child manifest without allowing a mutable tag to replace the base.
FROM alpine:3.21.7@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

# 固定的容器运行身份。宿主 bind mount 必须预先归属这组数字 UID/GID。
ARG APP_UID=10001
ARG APP_GID=10001
ARG TARGETPLATFORM

ENV TZ=Asia/Shanghai \
    APP_NAME=novaveil \
    NO_COLOR=1

# 仅安装运行时必须的工具:
#   - ca-certificates: 访问 HTTPS 上游。
#   - tzdata:          配合 ENV TZ 提供时区数据库。
# BusyBox 自带 wget, HEALTHCHECK 不额外安装 curl。
RUN apk add --no-cache ca-certificates tzdata \
 && rm -rf /var/cache/apk/* \
 && addgroup -g "${APP_GID}" -S novaveil \
 && adduser -u "${APP_UID}" -S -D -H -h /app -s /sbin/nologin -G novaveil novaveil \
 && mkdir -p /app/data \
 && chown "${APP_UID}:${APP_GID}" /app/data \
 && chmod 0700 /app/data

# 只 COPY 运行必需产物。应用与许可证保持 root 所有、只读; 仅 /app/data
# 归属应用用户, 防止运行时修改二进制或供应链报告。
COPY build/docker/${TARGETPLATFORM}/${APP_NAME} /app/${APP_NAME}
COPY LICENSE /app/LICENSE
COPY build/THIRD_PARTY_LICENSES.csv /app/THIRD_PARTY_LICENSES.csv
COPY build/THIRD_PARTY_LICENSES.frontend.json /app/THIRD_PARTY_LICENSES.frontend.json
COPY scripts/dockerfile/entrypoint.sh /entrypoint.sh

RUN chmod 0555 /entrypoint.sh /app/${APP_NAME} \
 && chmod 0444 /app/LICENSE /app/THIRD_PARTY_LICENSES.csv /app/THIRD_PARTY_LICENSES.frontend.json

USER ${APP_UID}:${APP_GID}
WORKDIR /app
EXPOSE 8080

# 镜像 wget 探测数据库探活端点
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["start"]
