# Agent Note: OpenCode 目录模型元数据展示

Status: implemented

## Problem

NovaVeil 同步 OpenCode 渠道模型时已拉取 `models.opencode.ai/api.json` 公开目录，但只提取了协议(chat/responses/anthropic)，丢弃了上下文窗口、输出上限、推理/工具调用能力与输入模态。管理员在管理台模型列表里只能看到模型名和协议，无法判断一个模型支持多大上下文、是否支持推理或工具调用、是否接受图片/音频输入，需要另开网页查目录。

## Decision

在同步流程中一并提取并落盘目录里的能力元数据，管理台模型列表以徽章展示：

- `ChannelModel` 结构新增五个字段：`ContextWindow`/`MaxOutput`(int, 0=未知)、`Reasoning`/`ToolCall`(bool)、`InputModalities`(逗号分隔 string)。GORM AutoMigrate 自动加列，默认值 0/false/空，存量行无破坏。
- `opencode_catalog.go` 把 `FetchOpencodeProtocols`/`ParseOpencodeProtocolCatalog` 重命名为 `FetchOpencodeCatalog`/`ParseOpencodeCatalog`，返回 `map[string]OpencodeModelInfo`（协议 + 元数据）。`opencodeCatalogModel` 结构补 `Limit`/`Reasoning`/`ToolCall`/`Modalities` JSON 字段。
- `sync.go` 在重建 auto 模型时从目录写入元数据；目录失败仍只跳过填充，不阻断同步。跳过条件从「无增删且协议不变」扩展为「无增删且协议与元数据均不变」，确保首次同步和目录元数据变更都能触发写入。
- `op/channel.go` 的 `syncChannelModels` 在更新已有模型和创建新模型时都写入元数据字段(原先只更新 `source`/`upstream_protocol`)。
- 前端 `ChannelModel` 类型加对应可选字段；`channel-editor.tsx` 模型行在模型名后展示徽章：`{ctx}K ctx`、`{out}K out`、`reasoning`、`tools`、非 text 模态名(image/audio/video/pdf)。`formatTokens` 把 976000 格式化为 "976K"、1024000 为 "1M"。

## Alternatives considered

- **只展示不落盘(前端直接拉目录)** — 避免改 DB schema；否掉是因为管理台与同步任务是两个独立入口，前端拉目录会重复请求且无法覆盖手动添加的模型，落盘后所有模型行统一携带元数据，展示不依赖网络。
- **加 `/v1/models` 端点给客户端用** — 对外暴露元数据；否掉是因为用户只要管理台展示，客户端端点是独立需求，不在本次范围。

## Consequences

- **收益**：管理员在模型列表即可看到上下文窗口、输出上限、推理/工具调用能力与多模态支持，无需另查目录。
- **代价与已知上限**：元数据只在 auto 模型同步时写入，手动添加的模型不带元数据(徽章不显示)；目录未提供的字段为 0/false/空，展示为无徽章。重访信号：OpenCode 目录变更字段名或结构。

## Verification

`internal/helper/opencode_catalog.go` 提供 `FetchOpencodeCatalog`/`ParseOpencodeCatalog` 返回 `OpencodeModelInfo`；单测 `internal/helper/opencode_catalog_test.go` 覆盖 zen/go 两档的协议 + 上下文窗口 + 输出上限 + 推理 + 工具调用 + 模态解析。`internal/task/sync_test.go` 的 `stubSyncFetch` 已适配新返回类型。前端 `pnpm typecheck` + `pnpm lint` 通过。`go build ./...`、`go test ./...`、`pnpm verify-notes` 通过。
