# Agent Note: go.mod 个人 fork 伪版本依赖风险记录

Status: implemented

> **部分取代**：2026-09-21 起 `gin-contrib/sse` 已切回官方 `v1.1.2`，本文中关于该 fork 的决定被 [2026-09-21-remove-gin-contrib-sse-fork-keep-go-sse-fork](./2026-09-21-remove-gin-contrib-sse-fork-keep-go-sse-fork.md) 接管；`tmaxmax/go-sse` 与 `axonhub/llm` 的保留决定及理由同样以新笔记为当前权威。

## 问题

2026-09-20 后续审计 DEV-04 指出：`go.mod` 直接依赖 `github.com/looplj/axonhub/llm v0.0.0-20260917094421-19a3c27d8b94`（伪版本），且 `replace github.com/gin-contrib/sse => github.com/looplj/sse v0.0.0-20260223020440-b463add2d52f` 与 `replace github.com/tmaxmax/go-sse => github.com/looplj/go-sse v0.0.0-20250909130008-e74a1155bc3b` 都指向个人 fork 的伪版本。伪版本无法表达可审计的语义版本，个人 fork 也不在官方供应链信任范围内。

## 决定

- 本次不做 `go.mod` 换源/删除 replace；这些依赖被产品代码实际使用，盲改可能破坏构建与行为。
- 检查结果记录（`go list -m -versions`）：`github.com/looplj/axonhub/llm` 没有 tagged versions，只有伪版本可用；`github.com/gin-contrib/sse` 官方有 `v0.1.0` 到 `v1.1.2`，`github.com/tmaxmax/go-sse` 官方有 `v0.1.0` 到 `v0.11.0`。这两个 fork 替换带有未明确说明的私有变更，切换上游版本需要逐项核对 fork 差异并跑 `go build ./...` + `go test ./...`。
- 风险缓释交给新增强的供应链基线：`security-scan.yaml` 周频 `go vet ./...` + `pnpm audit`；Dependabot 至少覆盖 gomod/npm 常规更新，能在可更新的依赖上暴露问题。风险本身保留，并在本笔记显式化。
- 后续处理方向：fork 中的定制点应向上游提交 PR 并切换回官方 tagged 版本；`axonhub/llm` 在没有 tag 前至少记录 fork 的变更内容并定期跟进。

## 备选方案

- 立即把 `gin-contrib/sse` 与 `tmaxmax/go-sse` 换回官方 tagged 版本：改动最小，但 fork 可能包含 SSE 行为/协议兼容的未合并补丁，直接切换有回归风险；在无法做完整差异审阅的窗口期未选。
- vendor 依赖到仓库：能冻结代码，但仓库体积增大、DIFF 噪音大，且不能解决伪版本审计问题；未选。
- 删除 `replace` 并以 `GONOSUMDB`/`GOPRIVATE` 忽略校验：只会削弱供应链保障，未选。

## 后果

- 收益：没有为“看起来整洁”的依赖列表牺牲可构建性；风险被写进笔记，未来维护者能理解为什么仍是伪版本/fork。
- 代价与边界：供应链风险存续，Dependabot 对伪版本与 `replace` 指向的个人 fork 通常无法提供有效的版本更新 PR；`security-scan.yaml` 的 `go vet` 也发现不了上游 fork 中的安全问题。这件事需要持续人工跟进，而不是已被自动化闭环。
- 触发条件明确：当上游 fork 的补丁被合入官方或 fork 停止维护时，应优先考虑替换回官方版本。

## 验证

- `go list -m -versions github.com/looplj/axonhub/llm` → 无 tagged versions。
- `go list -m -versions github.com/gin-contrib/sse` → `v0.1.0 v1.0.0 v1.1.0 v1.1.1 v1.1.2`。
- `go list -m -versions github.com/tmaxmax/go-sse` → `v0.1.0 ... v0.11.0`。
- `cd /workspace/NovaVeil && go build ./...` 复核：当前工作区尚有早前审计修复未完成接线，`internal/op` 报 `undefined: seal`/`err`；本笔记决定“不改 go.mod”本身不新增构建错误。