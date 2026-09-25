# Agent Note: 在网关层做请求脱敏与流式还原，而非独立中间人代理

Status: implemented

## 问题

使用 Cursor、Claude Code、Codex 等 AI 编程助手时，代码里的高危敏感信息（API Key、数据库连接串、内网 IP、手机号/身份证）会随请求发往外部模型服务商。需要在请求出网前本地自动打码成结构化占位符，模型回答时流式无感还原成原文，且开发体验不受影响。

NovaVeil 本身就是 LLM API 网关，客户端的 `base_url` 已指向它，天然处于请求必经之路。核心代码在 `internal/relay/mask/`，接入 `internal/relay/handler.go` 与 `internal/relay/mask_integration.go`，对应需求 REQ-015。设计参考 [Data Maskit](https://github.com/xiaYuTian11/maskit)（Python + mitmproxy）。

## 决定

在 NovaVeil 网关层（`relay/handler.go` 请求体读取后、`sendPassthrough` 前）插入脱敏，在响应回写前（非流式 `c.Writer.Write` / 流式 SSE 每个 event）插入还原，不引入独立中间人代理。六条硬约束：

1. **100% 本地运算零遥测**：脱敏与还原全在本地进程内，严禁用户追踪/云端日志。
2. **Fail-Closed 严格熔断**：脱敏管线未捕获异常或请求体超限时立即阻断，绝不放行未脱敏明文出网。
3. **占位符 `{{LABEL_6位辅音}}`**：后缀 6 位纯辅音随机串（`crypto/rand`），消除大模型对十六进制做变异算术的诱因；会话级滑动窗口复用保证多轮一致。
4. **流式中途无事件返回空**：严禁空字节导致 chunked 语法提前断连。
5. **凭据只存哈希**：API_KEY/TOKEN/SECRET/JWT 在事件库恒只存 preview + sha256 摘要，导出剔除原文。（注：此「事件库 + preview/sha256 摘要」管线实际未落地；命中明细对原文的持久化已由 [2026-09-17 翻转](./2026-09-17-mask-match-original-display.md) 改为存 label + original + placeholder。本条保留为历史约束表述。）
6. **默认全关，不可退化**：`MaskConfig.Enabled` 默认 `false`，setting 行不存在即视为关（无需初始化写入默认行）；分组开关 `GroupRelayConfig.MaskEnabled` 零值 `false`，旧分组 JSON 反序列化自动得关，无数据迁移；单规则开关出厂恒全 `false`。三层任一为 `false` 即整条链路跳过，关闭时热路径跳过正则与还原，但仍先读取并解析一次配置（不能宣称仅为一次 bool 判断或零开销，见脱敏 README §八.4）。

## 备选方案

- **复用 maskit 的 mitmproxy 独立代理** — 最强论据是现成实现可直接跑；否掉因为要向操作系统安装自签名 CA 根证书、多端口反代与 Passthrough 兜底，而 NovaVeil 已在请求必经之路且已有故障转移，重复造中间人复杂度，Go 正则在每请求热路径也比 Python 快。
- **在客户端做脱敏** — 最强论据是离敏感源最近；否掉因为客户端多样（Cursor/Claude Code/Codex 各异）且不可控，无法统一审计与规则升级，且客户端改动无法覆盖已发出的历史请求。
- **仅靠现有日志 redact（`op/error_log.go`）** — 最强论据是零新增代码；否掉因为日志 redact 管「记录」不管「出网」，明文请求体仍原样发往上游，没解决核心问题。两者互补：日志 redact 管记录，请求脱敏管出网。

## 后果

- **收益**：零额外部署组件，复用 NovaVeil 已有的会话（`X-Session-Id`）、审计、日志 redact 基建；Go 正则性能可接受；管理员按分组开关，粒度可控；出厂全关保证不影响存量流量。
- **代价与已知上限**：流式 SSE 跨 chunk 增量还原（`stream_restore.go` 缓冲拼接）是最难模块，OpenAI/Anthropic 事件结构不同需按协议适配；规则误报对网关多用户影响比单用户 maskit 更大，故默认全关由管理员按需开。重访信号：上游协议新增 SSE 事件类型、或误报率不可接受时需重访规则默认值与协议适配分支。**保护边界（2026-09 审计复核后拍板：预期行为，不修）**：脱敏的承诺是「命中原文不出网」（上游不可见），不是「原文在本地零落盘」——还原后的明文响应体按设计全量写入会话留档（`ConversationRecord.Response`，`data/conversations/*.jsonl`）与请求终态状态（内存 64 KiB 预览），请求侧留档则恒为占位符版；留档受 0600 权限、保留天数、目录硬预算与磁盘水位约束，可见性域与管理员本机审计一致。

## 验证

脱敏核心包 `internal/relay/mask/`（`rules.go`/`placeholder.go`/`engine.go`/`restore.go`/`stream_restore.go`/`session.go` + `*_test.go`）；配置 `internal/model/mask_setting.go`、`internal/op/mask.go`、`internal/server/handlers/mask.go`；前端 `web-next/src/pages/Mask.tsx`。出厂默认全关由单测守死（见脱敏设计文档第八节）。详细设计见 [docs/脱敏开发/](../../../../docs/脱敏开发/README.md)。
