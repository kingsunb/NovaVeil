# Agent Note: 从 magpie 移植纯正则脱敏规则扩充检测面，不引入本地 NER 模型

Status: implemented

## 问题

NovaVeil 出厂的 12 类内置脱敏规则（见 [02-规则引擎设计.md](../../../../docs/脱敏开发/02-规则引擎设计.md)）检测面有限，不含加密钱包私钥，也缺 SSN/IBAN/公网 IP/护照号等个人数据。这些恰是 AI 编程助手场景的高频泄漏物：开发者把钱包私钥、`.env` 里的 SSN、IBAN 转账串贴进对话并不罕见。检测面缺口真实存在，但补齐不能破坏 NovaVeil 的服务端网关定位（轻量、无大模型依赖、出厂全关不可退化，见 [2026-09-10 网关层脱敏决定](./2026-09-10-gateway-native-request-masking.md)）。

## 决定

从 magpie（MIT，https://github.com/yetone/magpie ）移植 15 条**纯正则 + 校验函数**脱敏规则，**不引入其本地 NER 模型**。规则分两组：

- **加密钱包私钥（11 条）**：`WIF_KEY`、`BIP32_KEY`、`SOLANA_KEY`、`SOLANA_KEYPAIR`、`STELLAR_KEY`、`SUI_KEY`、`APTOS_KEY`、`NOSTR_KEY`、`AGE_KEY`、`ZCASH_KEY`、`NEAR_KEY`。
- **个人数据补充（4 条）**：`SSN`、`IBAN`、`IP`（公网 IPv4）、`PASSPORT`（CJK 护照号）。

落地事实：

- 新增 `internal/relay/mask/rules_crypto.go`（11 条加密钱包）、`internal/relay/mask/rules_personal.go`（4 条个人数据），由 `rules.go` 接入四个挂载点：`BuiltinRules`（编译后正则与捕获组）、`BuiltinRuleMeta`（对外元信息与默认开关）、`builtinValidators`（校验函数，如 SSN 区段排除、IBAN MOD 97、IP 公网段排除）、`ruleMayHit`（特征预检，热路径先判可能命中再做正则）。
- 15 条全部 `DefaultEnabled = false`，延续 NovaVeil 出厂全关不可退化硬约束；管理员按需开启，与既有 12 类同走三层开关。
- MIT 许可证允许并入 AGPL 项目，来源与归属在 [02-规则引擎设计.md](../../../../docs/脱敏开发/02-规则引擎设计.md)「magpie 移植规则」子节注明。

## 备选方案

- **引入 magpie 完整 NER 模型（AstrLink Guard，约 1.6 GB）** — 最强论据是检测召回与上下文消歧最全；否掉因为 1.6 GB 模型加载、常驻内存与冷启动开销不符合 NovaVeil 服务端网关轻量定位，且引入大模型依赖违背「100% 本地零遥测、无重型依赖」的脱敏硬约束。
- **自研这 15 条正则与校验** — 最强论据是完全自主可控、无外部归属；否掉因为 magpie 已有成熟正则 + 校验且 MIT 可并入，重复造轮子违反「成熟轮子优先链」，自研还会重走其踩坑（如 IP 右边界、SSN 区段排除）。
- **不扩充，维持 12 类** — 最强论据是零变更零风险；否掉因为加密钱包私钥与 SSN/IBAN 等是 LLM 编程场景高频泄漏物，检测面缺口真实存在，维持现状等于放任这些明文出网。

## 后果

- **收益**：检测面扩充 15 类，覆盖主流加密钱包私钥与常见个人数据；出厂全关不破坏存量流量；MIT 合规并入 AGPL 并保留归属；纯正则 + 校验在 Go 热路径性能可接受，无大模型依赖。
- **代价与已知上限**：纯正则 + 校验的召回/精度不及 NER 模型（无上下文消歧），但这是网关定位下的有意取舍。**本次未移植**：`WALLET_KEY`（ETH `0x` 私钥）与 `SEED_PHRASE`（助记词）因需上下文校验（`at`/`fix` 类跨片段判定）而当前引擎只做单匹配区间替换，留待引擎支持上下文校验后补齐；`PASSPORT` 只做 CJK 形式，英文上下文形式同理待引擎扩展。`bound`（前后邻接字符排除）同属区间外判定，未移植；`SSN`/`IP` 已用「整段吞入 + 校验拒绝」等效防截半（2026-11 审计补齐），`PASSPORT` 等其余规则仍可能多掩或残尾。重访信号：引擎支持上下文校验后补 `WALLET_KEY`/`SEED_PHRASE`；某规则误报率不可接受时重访其默认值或正则收紧。

## 验证

新规则代码在 `internal/relay/mask/rules_crypto.go`、`internal/relay/mask/rules_personal.go`，接入点在 `internal/relay/mask/rules.go` 的 `BuiltinRules`/`BuiltinRuleMeta`/`builtinValidators`/`ruleMayHit`；15 条默认全关由 `BuiltinRuleMeta` 声明且单测守死（见脱敏设计文档第八节）。设计文档见 [docs/脱敏开发/02-规则引擎设计.md](../../../../docs/脱敏开发/02-规则引擎设计.md)「二、内置规则（含 magpie 移植扩充）」。许可归属见同节来源说明。
