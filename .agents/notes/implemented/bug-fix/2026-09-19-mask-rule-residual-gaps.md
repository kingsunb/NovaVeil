# Agent Note: 脱敏规则三处残留缺口修复（Stripe 预检 / MAC 截断 IPv6 / 同起点短匹配截长）

Status: implemented

## 问题

规则引擎重构为「候选收集 + 一次替换」（`maskCandidates`）并补齐 JSON 感知扫描后，针对审查清单做逐项实测（旧正则/旧算法复刻对比 + 新引擎行为断言），确认 8 项缺陷中 5 项已随重构修复（USCC 校验公式、SECRET 完整值、JSON 转义、Bearer 大小写与字符面、手机号 +86 整体替换、MAC 混合分隔符），但有 3 处残留：

1. **Stripe 前缀预检缺失**：`API_KEY` 正则已支持 `[sr]k_(live|test)_`，但特征预检 `apiPrefixes` 只含 `sk-`——纯 `sk_live_…` 文本不满足任何预检前缀，整条规则被跳过，密钥明文透出（实测复现）。
2. **MAC 截断 IPv6**：冒号形态 `{5}` 恰好匹配 6 组，对合法 IPv6（8 组冒号链）截取前 6 组产出 `{{MAC}}:77:88`（实测复现）。
3. **同起点短匹配截长**：候选排序把 `ruleIdx` 排在 `origEnd` 之前，同起点时先序规则（API_KEY 的 `sk-…`）压过后序规则更长命中（CONNSTR 完整密码含 `.tail`），替换后残留 `.tail`——与代码注释宣称的「长匹配优先，不以短词截断完整凭据」不符（实测复现）。

## 决定

- `apiPrefixes` 增补 `sk_live_`/`sk_test_`/`rk_live_`/`rk_test_`，预检前缀面与正则重新对齐。
- `MAC` 冒号形态改 `{5,}` 贪婪并新增 `macOK` 校验（冒号链须恰 6 组）：IPv6 整链成为一条候选后被校验整体拒绝、原样保留；恰 6 组的 MAC 照常脱敏。
- 排序键改为 `origStart → TERM → origEnd 降序 → ruleIdx`，同起点长匹配优先。

## 备选方案

- **Stripe 预检放宽（如 contains `sk_`）** — 最强论点是改一处字符串就行；否掉因为预检的意义是省正则扫描，粒度过松会让 API_KEY 对大量无关文本跑全量正则，按正则实际支持的前缀逐个登记才是正确对齐。
- **MAC 用负向断言排除第 7 组** — 否掉因为 Go RE2 不支持 lookahead/lookbehind，只能用「贪婪整链 + 校验拒长链」等效实现。
- **保持 ruleIdx 优先、单独把 CONNSTR 提前** — 最强论点是改动最小；否掉因为治标——任何两条规则同起点同场景都会复发，长度优先才是与注释一致的通用语义。

## 后果

- **收益**：Stripe/Restricted key 不再因预检缺口明文泄漏；IPv6 不再被截断误脱敏；同起点跨规则不再残留凭据尾部。
- **代价与已知上限**：MAC 后紧跟冒号十六进制续组（如 `aa:bb:cc:dd:ee:ff:80`，整链 ≥7 组）会被整条拒绝，MAC 本体不再单独脱敏——真实场景里 MAC 后直连冒号十六进制的基本只有 IPv6，取舍成立，重访信号是出现「MAC 后跟端口号形态」的真实误漏报案例。另两项实测确认的既有限制（非本次引入，已记入文档 02）：JSON 感知扫描的还原是纯文本替换、不按 JSON 重新转义，原文含引号/换行时回填破坏响应体转义（机密内容本身完整还原）；JWT 与 SECRET 同起点同长度时命中标签归 SECRET（内容整体脱敏，仅标签偏泛）。

## 验证

`internal/relay/mask/engine_test.go` 新增 4 条用例：`TestEngine_APIKey_StripePrefixMasked`（三种前缀明文透出即失败）、`TestEngine_MAC_IPv6NotTruncated`（8 组 IPv6 原样保留 + 正常 MAC 照常脱敏）、`TestEngine_Overlap_LongestCandidateWins`（CONNSTR `.tail` 不残留、JWT 三段不残留）、`TestEngine_JSONStringValuesMasked`（转义引号凭据与 unicode 手机号，此前零覆盖）。临时对比测试另实证了旧实现缺陷确实存在（旧 USCC 算法拒绝真实在册代码 `91330100799655058B`、旧 SECRET 只捕前缀、旧 TOKEN 不认全大写 BEARER、旧 API_KEY 不认 `sk_live_`、旧 MAC 截断 IPv6）。`go test ./internal/relay/mask/`、`go vet ./internal/relay/mask/`、`go build ./...` 全绿。
