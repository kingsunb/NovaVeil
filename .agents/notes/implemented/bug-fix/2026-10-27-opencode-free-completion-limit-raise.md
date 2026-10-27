# Agent Note: 免费档输出上限放宽到 128000, 体检保留 64000

Status: implemented

## Problem

OpenCode 免费档 `deepseek-v4.1-flash-free` 在长输入下连续失败, 转发日志形态一致:

- 输入恒为 42,914 token, 输出记 0, 失败原因 `Stream ended without finish_reason`, 首字 1~4 秒、总耗时 1m05s~1m55s 后被上游掐流。
- 相邻成功轮输入为 23,819 / 23,547 / 278, 分别产出 5,140 / 194 / 0 完成 token。
- 另有一次 `finish_reason=stop`、message 只有 `reasoning_content` 无正文的响应, 被既有 `errNoAnswerStop` 判空回答。

整形层只在客户端没带输出上限时补 `max_completion_tokens: 32000`(该值来自官方 2.1.x 客户端实抓)。reasoning 模型把 32000 消耗在思考上时, 正文没有余量, 表现为 reasoning-only 或提前断流。运营侧判断放宽上限可缓解。

## Decision

1. **转发上限 128000**: `opencodeV2CompletionLimit` 由 32000 改为 128000, v1 的 `max_tokens` 与 v2 的 `max_tokens`/`max_completion_tokens`/`max_output_tokens` 共用该常量, 客户端显式传值(含 `0`)仍一律保留。
2. **体检上限 64000**: 新增 `opencodeProbeCompletionLimit = 64000`, 作为 `ocProbeClientBody` 在 `maxOutput <= 0` 时的回退值。体检只判账号是否能通, 不需要和转发同等的输出预算。
3. **文档同步**: `docs/modules/relay.md` 与 `2026-10-22-opencode-free-client-fidelity.md` 的数值改为新值, 体检笔记的缺省值改为 64000。

## Alternatives considered

- **维持 32000(官方实抓值)** — 形态与官方逐字节一致, 是当前最强伪装; 否掉: 长思考模型在 agent 场景下正文拿不到预算, 表现为空回答轮次, 运营侧已实际损失请求。
- **按客户端已传值不补** — 尊重客户端意图; 否掉: 多数 agent CLI 不传输出上限, 不补会让上游按自身默认值判定, 形态与官方客户端不符。
- **做成设置项由管理员调** — 灵活; 否掉: 新增配置面与迁移成本, 当前只有一个运营方, 先固定值观测效果。

## Consequences

- **收益**: reasoning 模型有足够余额产出正文, 降低 reasoning-only 空轮次与提前断流的概率。
- **代价**: 偏离官方实抓的 32000, 若上游按「输入 + 输出上限」校验上下文窗口, 42.9k 输入配 128000 会更快触顶; 若上游对单轮输出有硬上限, 可能改为返回 400 或截断。
- **未验证**: 42,914 token 输入在 128000 下是否真能成功, 尚无常量落地后的实测; 失败是否与输出预算相关也未被单变量实验证实(失败轮输出记为 0, 更像是没拿到 usage 而非思考耗尽)。

## Verification

```sh
go build ./... && go test ./internal/relay/ -run 'Opencode|Probe' -v
```

- `TestShapeOpencodeFreeBody*` 与 `ocProbeClientBody` 相关用例已按 128000 / 64000 更新断言。
- 线上口径: 同一份 42,914 token 正文分别以 `max_tokens: 32000` 与 `128000` 单发, 对比是否从断流变为成功。
