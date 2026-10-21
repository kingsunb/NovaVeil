# Agent Note: OpenCode v2 免费档请求塑形对齐上游最终版

Status: implemented

## 问题

NovaVeil 的 OpenCode 兼容层已按 2026-10-15 的 v2 头集合对齐，但与上游 opencode2api `f0f064f` 的最终语义仍有五处偏差：

1. v2 子代理请求的 `x-opencode-session-id` 被 root 会话覆盖，客户端 child session 丢失。
2. 非空父会话在 v2 只写 `x-parent-session-id` 且不规范化，root/affinity 也没有切到父会话。
3. `global` 与 40 位小写十六进制项目号只在 v1 原样保留，v2 仍会哈希，和官方两档一致的提交号语义不符。
4. 免费档工具表缺少官方最终版的 `patch`（`patchText`）定义，客户端自带 `patch` 时无法校正。
5. v2 Anthropic 出站缺少 `beta=true`，与 2.x 客户端请求形状不一致。

这些偏差都在每次免费档请求上出现，属于可被上游指纹校验发现的稳定差异。

## 决定

### 会话头

`injectOpencodeCorrelationHeaders` 按档位分派：

- v2 先从出站头读取客户端 `x-opencode-session-id` 作为 child session；缺失时回退原始 `x-opencode-session`（父会话场景仍用原会话，而非父会话）。root 写入 `x-opencode-session`、`x-session-affinity` 与 `X-Session-Id`。
- 非空父会话统一用 `opencodeid.SessionIDFromSignal` 规范化为官方 `ses_` 形态，同时写 `x-parent-session-id` 与 `x-opencode-parent-session-id`；v2 的 root 改为父会话，child 保持客户端值。
- v1 保持原语义：`x-opencode-session-id` 镜像 `x-opencode-session`，不发 affinity 与 `X-Session-Id`，父会话仍双写规范化值。

调用顺序不变：`applyChannelConfig` 先完成自定义头、随机头与最终会话号，再补关联头。

### 项目号

`opencodeid.ProjectIDForLane` 在两档都原样返回 `global` 与 `^[0-9a-f]{40}$` 的 git 提交号；其余信号继续走域分隔 SHA-256 并截取前 20 字节，`prj_` 前缀因此仍被哈希掩盖。

### patch 工具

`opencode_tools.json` 从上游 `f0f064f` 逐字节加入 `patch` 定义，整个文件与该提交的最终版一致。`knownOpencodeTool` 因此能识别客户端自带的 `patch`；`ensureOpencodeFreeTools` 的同名替换条件从“仅比较 description”改为“description 与 parameters 都一致才保留”，避免同描述不同 schema 漏校正。`opencodeFreeCoreTools` 仍只有 `shell/edit/glob/grep/read`，`patch` 不参与缺失注入。

### v2 Anthropic beta

NovaVeil 的 v2 Anthropic 路径可达：内置 OpenCode Free 渠道开启 `OpencodeCompat`，模型目录把 `@ai-sdk/anthropic` 映射为 `anthropic`，模型协议落库后由 `modelProtocolProvider` 选到 Anthropic 转换器。用户自建渠道的 `OpencodeCompat` 在创建时被强制关闭，因此这条路径只经内置渠道的模型协议进入。`applyOpencodeFreeLaneHeaders` 在 v2 档且模型协议为 `anthropic` 时补 `beta=true`；值写入请求 Query，已有 URL query 中的 beta 会先移除再统一追加，其它 query 原样保留，重复调用不会产生重复项。

## 备选方案

- **v2 继续让 `x-opencode-session-id` 镜像 root**：实现最简单，但子代理会丢掉客户端 child identity，否掉。
- **父会话只写 `x-parent-session-id` 并保留原始值**：少一次规范化，但上游 2.x 会把父会话纳入 root/affinity，且要求官方会话格式，否掉。
- **两档继续哈希 `global` 与 git 提交号**：能保持旧行为，但与上游最终语义冲突，且 `global` 被哈希后不再是官方值，否掉。
- **把 `patch` 加入核心工具集**：会让所有免费请求无条件注入 `patch`，与官方“客户端自带才校正”的形状不符，否掉。
- **直接拼接 URL 字符串追加 `beta=true`**：实现短，但会破坏已有 query，也会在重复调用时产生重复参数，否掉。

## 后果

- **收益**：v2 免费档的 child/root 会话、父会话、项目号、`patch` 定义与 Anthropic beta 均与上游最终版对齐；v1 既有行为不变。
- **代价与已知上限**：同名已知工具的校正现在也看 parameters，客户端同描述不同 schema 时会被重写；这是上游逐字段校验所需，但会增加少量请求体差异。`beta=true` 只在模型表能明确给出 `anthropic` 时添加，无法识别的模型仍按 chat 处理。

## 验证

- `internal/relay/random_headers_test.go` 覆盖 v2 root/child、父会话双头与确定性、v1 不退化、两档项目号保留。
- `internal/utils/opencodeid/opencodeid_test.go` 覆盖 `global`/40-hex 保留与 `prj_` 哈希。
- `internal/relay/opencode_free_test.go` 覆盖 `patch` 定义识别、客户端 `patch` 校正、v2 Anthropic beta 的 query 保留与幂等、chat/v1 不补 beta。
- 统一验证命令：`go test ./internal/relay ./internal/utils/opencodeid`。
