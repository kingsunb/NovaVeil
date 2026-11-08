# Agent Note: 移除账号体检页的独立密钥渠道测试卡

Status: implemented

## 问题

keyed 渠道接入按 Key 分区的账号池后，账号体检页曾额外列出所有 keyed 渠道并调用 `/channel/test_keys` 逐 Key 测试。该入口不要求渠道启用、不要求配置含 `{account}` 的代理，也不走账号池、冷却或台账，测试结果无法反映真实转发健康度；同时它与页面主流程「选择可入池渠道后按 Key 体检账号池」重复，容易让管理员误把一次性 Key 测试当成账号可用性证据。

## 决定

- 删除 `web-next/src/pages/ProxyAccounts.tsx` 的 `KeyedChannelSection`、`keyedChannels` 过滤与渲染，以及仅该卡片使用的 locale 文案；页面回到「无可入池渠道则显示空态」的单一结构。
- 保留 keyed 渠道在主 `ChannelSection` 的账号池流程：Key 下拉、固定 `Key.ID` 账号、`key_id` 体检与台账/删除不变。
- 渠道编辑器原有的逐 Key 测试（`api.testChannelKeys`）保留，仅作渠道诊断，不写 IP 台账。
- 台账派生同步调整：网络错误计入不可用，并保留网络错误子集筛选；网络错误后出现更晚的真实成功时恢复可用。

## 备选方案

- **保留卡片但只列启用且配置 `{account}` 的渠道**：仍会形成第二套「体检」入口，且它不写账号池台账，结果与账号健康度口径不一致；不选。
- **把卡片结果写入账号台账**：逐 Key 测试不经过账号池选号，无法归属到具体出口账号，写台账会污染账号状态；不选。
- **只保留卡片作为纯诊断**：渠道编辑器已有逐 Key 测试，重复入口只增加维护面；不选。

## 后果

- **收益**：账号体检页只有一个入池渠道体检流程，Key 测试与账号健康不再混淆；页面与测试少一个组件、一组文案和异步竞态处理。
- **代价与已知上限**：需要单独检查某把 Key 是否可用时，要去渠道编辑器做逐 Key 测试，账号体检页不再提供该快捷入口。相关设计见 [keyed 渠道决策](../feature/2026-11-06-opencode-keyed-free-channel.md) 与 [账号体检记录](../feature/2026-10-24-opencode-proxy-account-probe.md)。

## 验证

```sh
go test ./internal/relay/...
cd web-next && pnpm test -- ProxyAccounts Dashboard
cd web-next && pnpm typecheck
pnpm verify-notes
```
