# Agent Note: 修正 G4F Space Groq 模型同步 URL

Status: implemented

## 问题

内置渠道 `G4F Space Groq Free` 的模型同步持续返回 HTTP 404，Groq 上游错误为 `GET /openai/v1/v1/models`。同一批其它 g4f.space 渠道同步正常，说明域名可达，问题在 Groq 代理路径与 NovaVeil 的版本拼接规则叠加后产生了重复的 `/v1`。

## 决定

`G4F Space Groq Free` 的 `BaseURL` 从 `https://g4f.space/api/groq` 改为 `https://g4f.space/api/groq/openai`。模型同步经 `NormalizeBaseURL(BaseURL, "v1")` 后请求 `/api/groq/openai/v1/models`，g4f.space 再映射到 `https://api.groq.com/openai/v1/models`。转发路径也由同一 `BaseURL` 得到标准的 `/api/groq/openai/v1/chat/completions`。

迁移 22 按 `builtin = true`、固定渠道名和旧 `BaseURL` 精确匹配，就地更新存量记录。新库由内置定义直接得到新地址；管理员自定义渠道和非 Groq 内置渠道不受影响。

## 备选方案

- **使用 `https://g4f.space/api/groq#` 并只请求 `/api/groq/models`** — 该地址的模型列表确实返回 200，但会绕过标准 `/v1` 路径，使模型同步与 OpenAI 兼容转发采用两套不一致的代理语义，因此不选。
- **在 provider 拼装层按 `g4f.space` 主机和 `/api/groq` 路径特判** — 能把异常集中在代码里，却把单个渠道的上游路径约定写进通用拼装逻辑，也会影响管理员自定义的同域渠道，因此不选。
- **下架该内置渠道** — 上游模型列表实际可用，404 不是服务下线，下架会丢失可用能力，因此不选。

## 后果

- **收益**：Groq 模型同步与转发路径恢复标准 OpenAI 兼容形态，存量部署升级后无需手工改渠道。
- **代价与已知上限**：内置渠道的可见 `BaseURL` 发生变化，依赖旧地址的外部脚本需要同步调整；迁移只识别精确的旧地址，曾被其它机制改写过地址的记录仍需人工处理。
- **重访信号**：g4f.space 调整 Groq 路由前缀，或 `/api/groq/openai/v1/models` 不再返回 OpenAI 模型列表结构。

## 验证

- `curl -i https://g4f.space/api/groq/v1/models` 返回 404，`x-url` 为 `/openai/v1/v1/models`。
- `curl -i https://g4f.space/api/groq/openai/v1/models` 返回 200，`x-url` 为 `https://api.groq.com/openai/v1/models`。
- `go test ./internal/helper ./internal/db/migrate` 覆盖最终请求路径和迁移幂等性。
