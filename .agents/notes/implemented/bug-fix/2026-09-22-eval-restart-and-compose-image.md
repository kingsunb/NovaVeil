# Agent Note: 评估崩溃重入与 Compose 镜像插值

Status: implemented

## 问题

`PopNext` 把任务置为 `running` 时只写一次 `started_at`。`ModelEvalQueueResetRunning` 只回收 `started_at` 为零或超过 15 分钟租约的行。进程崩溃或被 SIGKILL 后，任务停在 `running`；默认单实例 SQLite 也要等这 15 分钟，到期才重新入队打上游。

`cmd/start.go` 给评估停机钩子 5 分钟上界，`docker-compose.yml` 的 `stop_grace_period` 是 90 秒。宽限先到时 Docker 发 SIGKILL，worker 来不及把未完成的行定稿，下次启动又落入上面的租约。

`docker-compose.yml` 把 `${NOVAVEIL_WEB_NEXT_IMAGE:?...}` 放在顶层锚点。Compose 先对整个文件插值，再按 profile 过滤，所以没开 `web-next` profile 的 `docker compose config` 也会因这个变量失败。

周期清扫仍用 15 分钟租约，见 [评估队列并发与停机顺序](2026-09-21-eval-queue-cache-atomic-refresh-shutdown-and-relay-state-bounds.md)。本篇只覆盖启动/停机回收和 Compose 插值。

## 决定

- SQLite（以及其它非 MySQL/Postgres 方言）在 `Scheduler.Start` 调用 `ModelEvalQueueResetRunningOnStart`，立刻把全部 `running` 退回 `queued`，并清空 `started_at`。不获取入队用的 `GET_LOCK` / `pg_advisory_lock`。
- MySQL / Postgres 的启动回收仍走 `ModelEvalQueueResetRunning`：只回收零值或过期租约，避免抢走其它实例仍在执行的任务。入队锁保持原样。
- 调度器每分钟的 `reapTicker` 在所有方言上都继续只回收过期租约，避免把本进程正在跑的评估清掉。
- `Scheduler.Stop` 先取消上下文并等 loop 与 worker 退出，再 `ModelEvalQueueRequeueRunning` 本进程 `inflight` 里仍为 `running` 的 id。停机时若上游请求还没发出，或 `TestChannelKeyFailover` 因取消返回，worker 不写失败历史、不 `MarkDone`。`WHERE status = running` 保证已 `done` 的行不会再入队。其它实例的 `running` 不在 `inflight` 里，停机不动它们。
- `stop_grace_period` 保持 90 秒。评估钩子的 5 分钟只是 worker 不返回时的保险丝，不再用来覆盖一次完整评估。
- `NOVAVEIL_WEB_NEXT_IMAGE` 从顶层锚点移到 `web-next` 服务，写成 `${NOVAVEIL_WEB_NEXT_IMAGE:-}`。未设置变量时镜像名为空。profile 未启用时该服务在一致性检查前被禁用，默认 `docker compose config` 不依赖这个变量。启用 profile 且镜像名为空时，Compose 因服务既无 image 也无 build 而拒绝。`.github/workflows/build.yaml` 里仅为绕过 `:?` 的 `NOVAVEIL_WEB_NEXT_IMAGE` export 已删除；`NOVAVEIL_IMAGE` 仍是默认服务的必填插值，继续 export。

## 备选方案

- **把 `stop_grace_period` 调到不短于 5 分钟** — 能让钩子的上界在 SIGKILL 前走完，但单次评估最长 10 分钟，5 分钟宽限仍会在请求中途杀掉进程，而且每次发布都要空等。停机把未完成行退回 `queued` 更短，也覆盖 SIGKILL 之后的 SQLite 启动回收。
- **所有方言的 `ResetRunning` 都立即回收** — 崩溃恢复最简单，但 MySQL/Postgres 上新实例启动会把其它健康实例正在跑的评估抢走再打一次上游。
- **让 SQLite 也先拿入队锁再回收** — 多实例路径已经有 `GET_LOCK` / advisory lock，但 SQLite 单实例拿不到这些锁，回收不能依赖它们。
- **把 `:?` 挪到单独的 compose 文件** — 默认文件确实不再插值，但 `docker compose --profile web-next` 不再自动带上该文件，启用 profile 时镜像名约束和现有启动方式会分开。空默认值留在带 profile 的服务上：必填插值离开默认解析路径，启用 profile 时仍然没有镜像名就失败。

## 后果

- **收益**：单实例崩溃或 SIGKILL 后，下次启动马上把遗留 `running` 退回 `queued`，不再干等 15 分钟。优雅停机不重跑已经 `MarkDone` 的任务，也不动其它实例的 `running`。未启用 `web-next` 时，`docker compose config` 不再要求 `NOVAVEIL_WEB_NEXT_IMAGE`。
- **代价与已知上限**：崩溃发生在上游已接受请求、本进程尚未 `MarkDone` 时，重启仍会再打一次上游；这是未定稿任务的重试，只是不再延迟 15 分钟。多实例若在退回 `queued` 之前被 SIGKILL，未过期的 `running` 仍要等租约。`stop_grace_period` 短于 5 分钟保险丝：worker 若不响应取消，90 秒后仍会被 SIGKILL，SQLite 靠下次启动回收。`MarkDone` 失败时任务留在 `inflight`，停机会把它退回 `queued`，可能和已经写入的评估历史并存。

## 验证

- `internal/op/model_eval_queue_test.go`：`TestModelEvalQueueResetRunning` 仍只改 status、不写 `StartedAt`。`TestModelEvalQueueRestartResetRecyclesUnexpiredLease` 在租约未过期时确认 `ResetRunning` 不回收，单实例 `ResetRunningOnStart` 立即退回 `queued`，`done` 行保持完成。
- `internal/eval/scheduler_restart_test.go`：取消后的 `executeTask` 不打上游、不 `MarkDone`；`Stop` 只退回本进程未完成行。
- `docker-compose.yml` 中不再有 `NOVAVEIL_WEB_NEXT_IMAGE:?`。变量只出现在 `profiles: ["web-next"]` 的 `web-next.image`，形式为 `${NOVAVEIL_WEB_NEXT_IMAGE:-}`。
