# Agent Note: Bun ClientHello 签名算法表实抓更新（免费档 429 指纹墙破案）

Status: implemented

## Problem

2026-10-24 起免费档大面积 429，上游话术恒为 `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached..."}}`。用户侧现象是「账号体检显示健康，真实转发立刻 429」，指向体检与转发形态不一致的怀疑；远程受控实验证明怀疑不成立，真因在 TLS 指纹层：

1. **形态排除**：用 NovaVeil 出站链（BunTransport）分别发「官方逐字节体检模板」与「下游整形形态」，同出口同账号同 429——429 与请求体/头形态无关，体检只是先撞在额度尚存的窗口。
2. **真客户端对照**：同一分钟、同一 resin 出口，真 opencode 客户端（1.19.4 经匿名 HOME）拿 200，NovaVeil 拿 429——上游能区分两者。
3. **逐字节定位**：本地 TLS 观察代理分别终结 NovaVeil BunTransport 与真客户端（2.1.4 与 1.19.4）的 ClientHello 原始字节，除随机字段（random/session_id/key_share 公钥/SNI 域名）外唯一稳定差异是 signature_algorithms：真客户端 11 项，含 `Ed25519` 与 `ECDSAWithP521AndSHA512`（位于 PKCS1WithSHA512 之后、PKCS1WithSHA1 之前）；本仓 spec 抄自 Bun 1.4.3，只有 9 项。
4. **修复闭环**：spec 补齐两项后，此前 429 的出口（真客户端刚成功过的 IP）立即恢复 200；同出口改回旧 spec 复测仍 429。
5. **剩余 429 归因**：补齐后全新出口仍 429 的账号，真客户端同账号也失败（换一个全新账号真客户端成功）——即 FreeUsageLimitError 的另一半是按出口 IP 的免费额度耗尽（住宅代理池 IP 全球共享，多数已被耗尽），与客户端身份无关，换号即解。

## Decision

`internal/client/bun_dial.go` 的 `bunClientHello` 在 `SignatureAlgorithmsExtension` 的 `PKCS1WithSHA512` 之后、`PKCS1WithSHA1` 之前插入 `utls.Ed25519` 与 `utls.ECDSAWithP521AndSHA512`，对齐 2026-10-24 从官方 2.1.4 与 1.19.4 客户端实抓的 BoringSSL 指纹；函数注释补充实抓来源与 429 因果，防止下次升级再漂移。

## Alternatives considered

- **改用真 opencode serve 子进程池做免费档通道**（oc-bridge 形态，容器内已验证可用） — 最强论据是指纹天然保真、升版免维护；否掉作为本次修复：改动是整条出站架构替换，远超一行指纹对齐，且 BunTransport 在 HTTP 层已逐位对齐（见 [指纹实抓校准笔记](./2026-10-24-opencode-fingerprint-recapture-alignment.md)），只剩 sigalgs 一处漂移。
- **把 429 一律判为 IP 额度耗尽、只做换号治理** — 有 resin 日志的 IP 维度证据支持；否掉：同一 IP 上真客户端 200、本仓 429 的对照直接证伪「纯额度论」，不修指纹换多少号都会撞墙。
- **升级整份 spec 到最新 Bun 全量重抓** — 一步到位；否掉：实抓 diff 显示除 sigalgs 外其余字段（密码套件、曲线、扩展序、ALPN、psk 模式）完全一致，最小变更只动两行。

## Consequences

- **收益**：免费档出站与官方客户端 TLS 指纹逐字段一致；指纹墙导致的 429 消除（v1 档同 IP 429→200 已实测闭环，v2 档指纹同样对齐）。
- **版本同步清单（升级必查三处）**：`internal/client/bun.go` 的 `bunRuntimeVersion`（UA 回退 `Bun/<version>`）、`internal/client/bun_dial.go` 的 `bunClientHello` 指纹与注释、`internal/model/opencode_protocol.go` 的 `opencodeV1BunVersion`。2026-10-24 一并统一到 `1.4.4`：官方 2.1.4/1.19.4 客户端（跑 Bun 1.4.4）实抓 ClientHello 与本 spec 逐字段一致，即 Bun 1.4.3→1.4.4 的 TLS 指纹仅 sigalgs 两项之差，骨架未变。该清单已固化为 [AGENTS.md 约定](../../../../AGENTS.md)。
- **代价与已知上限**：剩余 429 属出口 IP 免费额度耗尽（住宅池 IP 共享所致），账号体检的换号机制是既有对策，不是代码缺陷；上游未来再改 ClientHello 时需重抓对比，重访信号是「真客户端 200 + 本仓 429」的对照再次出现。
- **判定口径**：排查 429 时先看错误体——`FreeUsageLimitError` 且真客户端同出口也 429 是 IP 额度；`FreeUsageLimitError` 但真客户端同出口 200 是指纹回归。

## Verification

```sh
go build ./... && go test ./internal/client/ ./internal/relay/
```

- 远程开发机实测记录（2026-10-24）：旧 spec 对有额度出口 429、补齐两项后同出口 200，回退即复现 429。
- `TestBunRequestHeadOfficialClientWire` 等既有指纹测试不受影响（本次只动 TLS 握手层，不动 HTTP wire）。
