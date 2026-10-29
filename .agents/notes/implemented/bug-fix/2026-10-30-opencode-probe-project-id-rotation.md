# Agent Note: 探测项目 ID 每轮随机铸造

Status: implemented

## 问题

账号体检（探测）的项目 ID 此前是进程级稳定固定值（`sha256("novaveil-opencode-account-probe")` 的 40-hex）。免费层把「会话 + 项目」组合身份挂在额度/风控记账上：固定项目号使探测身份跨轮累积记账，健康账号可能被误判不可用。参考 opencode2api 同期修复：「可用性探測每次使用新鮮規範會話與隨機項目 ID」。

## 决定

`internal/relay/opencode_account_probe.go`：

- 删除固定 `ocProbeProjectID`，新增 `newOcProbeProjectID()`：crypto/rand 20 字节 → 40 位小写 hex，形态与官方项目号一致；
- 铸造边界 = **一次探测运行**：`prepareAccountProbe`（同步入口 `ProbeOpencodeAccounts` 与后台作业 `StartOpencodeAccountProbe` 的共同路径）在计划阶段铸一次写入 `ocProbePlan.projectID`，轮内全部账号共享，跨轮更换——对齐官方语义「一个项目 = 一个目录」；
- 签名串联 `runAccountProbe → ocProbeOne → ocProbeRawRequest` 传递，v2 档 `x-opencode-project` 头取该值。

会话 ID 经确认本就每请求新铸且不进缓存，未改动。

## 备选方案

- **每请求一个项目 ID**：同一轮内账号各自携带不同项目号，违背官方「一轮 = 一个项目目录」的形态语义，否。
- **维持固定 + 定期轮换**：轮换周期内仍会累积记账，且引入配置复杂度；随机铸造零状态。

## 后果

- **收益**：探测身份不再跨轮累积，健康账号的可用性判定不被历史记账污染。
- **代价**：上游侧项目维度的 prompt cache 亲和每轮从零开始（探测请求本身无缓存收益预期，可忽略）。
- **范围外提示**：体检模板的 `{{WORKDIR}}` 仍取进程稳定 cwd——若日后实证上游记账包含 workdir 维度，需另行评估。

## 验证

```sh
go test ./internal/relay/ -run 'TestNewOcProbeProjectID|TestOcProbeRawRequestMatchesRealClient|TestProbeOpencodeAccountsSendsClientFormAndUniqueSessions|TestStartOpencodeAccountProbeRunsInBackground' -count=1 -v
```

- 形态合法（`^[0-9a-f]{40}$`）+ 同轮稳定 + 跨轮不同 + 构造器对合法值原样透传。
