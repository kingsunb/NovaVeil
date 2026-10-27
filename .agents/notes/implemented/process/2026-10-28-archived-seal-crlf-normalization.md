# Agent Note: 归档封印按内容哈希, 忽略检出换行

Status: implemented

## 问题

`pnpm verify-notes` 在 Windows 开发机上长期报 `archived/... — seal mismatch: archived note was modified after sealing`，而该归档笔记在 git 里没有任何改动。

根因是校验脚本 `scripts/agent-notes/verify-archived-agent-notes.ts` 的 `sealOf` 直接对 `readFileSync` 返回的原始字节取 sha256。仓库没有 `.gitattributes`，Windows 默认 `core.autocrlf=true` 会把笔记检出成 CRLF，而 manifest 里的封印是提交时的 LF 字节哈希，两者自然不等。同一脚本内的头块检查早已用 `replace(/\r\n?/g, "\n")` 归一化，只有封印计算漏了这一步。

后果不是单一的假红：门禁长期失败会让真实的"归档后被改动"淹没在噪声里，等于封印机制失效。

## 决定

`sealOf` 在哈希前把内容按 `\r\n?` → `\n` 归一化，与头块检查共用同一口径。LF 工作区算出的哈希与改动前完全一致，manifest 与归档笔记均未修改。

## 备选方案

### 为什么不用 `.gitattributes` 强制归档目录 `-text` / `eol=lf`

会改变全仓检出行为，已检出的文件需要整批重新归一化重新提交，影响面远大于脚本本身，且换行策略属于团队环境决定，不该由一次门禁修复顺手定下。

### 为什么不用 `--write` 重新封印

重封会把 seal 改成 CRLF 版本哈希，与 HEAD 基线不一致，append-only 检查照样失败；更糟的是它等于用"重新盖章"掩盖一次封印不符，违背归档即冻结的初衷。

## 后果

- **收益**：Windows 与 CI（LF）口径一致，`pnpm verify-notes` 不再因环境换行报假红，真实封印破坏才能被看见。
- **代价**：封印不再能检测"只有换行被改动"的编辑。这类改动不含语义，且 git 层面可见，可接受。
- **已知上限**：BOM 仍在哈希范围内，带 BOM 与不带 BOM 的同一内容仍算不同封印，保持既有行为。

## 验证

```
pnpm verify-notes
```
