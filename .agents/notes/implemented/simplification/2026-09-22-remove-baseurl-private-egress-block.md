# Agent Note: 移除渠道 BaseURL 私网出口限制

Status: implemented

## 问题

原渠道上游 `BaseURL` 做完整 SSRF 出口校验（SEC-01 的原始实现见 [同日 bug-fix 笔记](../bug-fix/2026-09-21-relay-egress-header-mask-and-test-token-audit-fixes.md)）：`internal/op/channel.go` 里的 `validateChannelEgressBaseURL` 拒绝解析到私网/环回/链路本地/未指定/组播/CGNAT/元数据/保留段的地址。这把「docker 网络内的自建网关（如 `http://grok2api:8000`）」这类合法内网上游也一并拦下，管理员必须绕到公网地址或改配置才能用。同类项目 new-api 对渠道 `BaseURL` 同样不做地址范围限制——管理员可信，直连上游不拦截；它的 SSRF 防护只作用于「抓取 prompt 给的任意 URL（fetch/download/doc-parse/image/mj-proxy）」这类用户可控地址。

## 决定

移除渠道 `BaseURL` 的目标地址范围限制，`BaseURL` 不做地址范围校验：

- `validateChannelBaseURL` 与导出的 `ValidateChannelEgressBaseURL` 都收敛为 `return validateChannelBaseURLSyntax(raw)`，只强制非空、http/https scheme、`host` 存在、无 `userinfo`。
- 删除 `validateChannelEgressBaseURL`、`validateEgressHost`、`reservedIPv4Blocks`、`validateEgressIP`、`embeddedTunnelIPv4`、`isTestBinary` 及其 `net`/`flag` 依赖；`internal/conf` 不再参与。
- 内部测试与非测试不再有行为差异：`isTestBinary`（`flag.Lookup("test.v")`）随之删除。
- 撤销上一轮临时加的布尔开关 `security.allow_private_upstreams`（字段、默认值、env `NOVAVEIL_SECURITY_ALLOW_PRIVATE_UPSTREAMS`、`strconv` 依赖），它被「干脆不做限制」取代。
- 保留导出名 `ValidateChannelEgressBaseURL` 以兼容调用方：`internal/server/handlers/channel.go`（fetch-model 两处）、`internal/task/sync.go`、`internal/op/backup.go`，其语义变为「只做格式校验」。

## 备选方案

- **保留严格拒绝（原 SEC-01）**：维持 SSRF 基线，但挡内网自建上游这一真实部署；已否决。
- **布尔开关 `allow_private_upstreams`（上一轮临时方案）**：放开内网却用开关一刀切切换，而需要私网上游的部署反而常见，开关徒增配置面；已否决。
- **CIDR/IP 白名单**：粒度细但给运维新增信任段维护；与 new-api 的「渠道上游不拦截」对齐更简单；未采用。

## 后果

- **收益**：内网上游零配置直连（`http://grok2api:8000` 直接作为渠道 `BaseURL`），删除 `conf`/`net`/`flag`/`time` 与整套 IP 段判定，暴露面与测试更小。
- **代价/风险**：直连渠道成为对上游的 SSRF 客户端——能创建/编辑渠道者即可让网关访问任意内网主机与云元数据端点（凭据用该渠道自己的 Key）。渠道写权限须视为高权；部署方应隔离网关到内网上游的网络。`docs/SECURE_DEPLOYMENT.md` 已改写为陈述这一取舍。
- **边界**：`channel_proxy`（出站代理）本就允许私网/环回，不受此变更影响；两者现在都不做地址范围限制。

## 验证

- `internal/op/channel_url_test.go` 改写：私网/环回/链路本地/保留段/内网域名 `BaseURL` 全部接受，仅 `ftp://`、裸主机、空串、userinfo 拒绝。
- `internal/op/seal_at_rest_test.go` 的 `TestChannelCreateAllowsPrivateChannelProxy` 由「环回/私网拒绝」改为「接受」。
- `go build ./...` 与受影响包 `go test ./internal/op/` 通过。