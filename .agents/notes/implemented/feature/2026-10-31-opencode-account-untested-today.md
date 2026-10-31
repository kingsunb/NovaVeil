# Agent Note: 账号台账「未测试」改为「今日未测试」

Status: implemented

## Problem

每日批量体检后，运营需要回答「今天还有哪些账号没测过」。原「未测试」只覆盖从未体检且从未成功的账号，一旦体检过就永远退出该分类，无法按自然日找出今天遗漏的账号；页面上的「未测试」也容易被误解为当前完全没测过。

## Decision

`GET /channel/proxy-accounts?filter=untested` 的筛选值与 JSON 字段 `untested` 保持不变，口径改为「今日未测试」：账号的最近体检时间（`last_probe_at`）与最近成功时间（`last_success_at`）都不在服务端本地自然日的今天。

最近成功包含真实转发成功与体检成功；体检成功同时写 `last_probe_at`，所以判定等价于「今日既无体检也无成功」。

台账计数 `ledger.Untested`、筛选 `untested`、前端 chip 与行内 `state="untested"` 标签统一显示「今日未测试」。行内四态派生不变：`state="untested"` 仍只表示从未体检且从未成功；今日未测试集合可与可用/不可用/网络错误重叠（例如昨日体检健康、今日未复测的账号仍显示可用，但会进入今日未测试筛选）。

日期边界按服务端本地自然日（`time.Local`），与成功天数 `success_days` 的 `2006-01-02` 口径一致；不新增数据库列、迁移或配置项。

实现位于 `internal/relay/opencode_account_probe.go` 的 `ocAccountTestedToday` 与 `ListOpencodeAccountStates`；前端仅改 locale 文案与测试 mock，筛选 token 不变。

本篇部分取代 [OpenCode 代理账号批量体检](2026-10-24-opencode-proxy-account-probe.md) 中「未测试=从未探测且从未成功」的台账口径；其余体检、台账与自动体检决定仍有效。

## Alternatives considered

- **只按 `last_probe_at` 判断** — 实现更直接，但今天已有真实转发成功的账号其实已被验证，仍归入今日未测试会诱导重复体检；否掉。
- **新增独立筛选值或 JSON 字段 `untested_today`** — 语义更显式且不改变旧字段含义；但该接口只有本仓库前端消费，没有外部兼容压力，新增字段会同时扩大后端契约、前端类型与测试面积；保留 `untested` 并更新注释与测试更小。
- **只改筛选 chip，行内状态仍显示「未测试」** — 改动最小；但同一页面两处文案指向不同口径会造成误读，用户明确要求行内同步；否掉。

## Consequences

- **收益**：台账可以直接回答「今天哪些账号还没测」，批量体检后能一键筛出遗漏账号；今日已有真实成功的账号不会再被误报为未测试。
- **代价与已知上限**：`untested` 不再是互斥状态，计数之和会大于 `total`（与历史可用类似）；今日未测试集合会包含可用、不可用或网络错误账号。口径随服务端时区在本地 0 点滚动，跨时区部署或修改进程时区会改变「今日」边界；需要按用户时区切日时得另加时区配置。
- **重访信号**：若外部消费者依赖 `untested` 的旧语义，或需要按浏览器/渠道时区而非服务端本地时区切日，需重新评估契约与时区来源。

## Verification

```sh
go test ./internal/relay -run TestOpencodeAccountLedgerStates
pnpm -C web-next test -- ProxyAccounts
pnpm verify-notes
```

- `TestOpencodeAccountLedgerStates` 覆盖：昨日健康但今日未测 → 今日未测试；昨日被拒但今日成功 → 已测试；冷却或从未探测 → 今日未测试；筛选与删除后的计数。
- 前端用例覆盖 chip 文案「今日未测试」、计数，以及点击筛选后今日已测的可用账号不出现在台账。
- `pnpm verify-notes` 校验笔记树、格式与互链。
