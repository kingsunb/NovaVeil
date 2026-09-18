# NovaVeil 文档索引

## 从这里开始

- **当前项目规划**：[ROADMAP.md](ROADMAP.md)，汇总代码现状、下一阶段优先级和验收边界（2026-09-16 源码核对）。
- **功能记录**：[FEATURES.md](FEATURES.md)，查看需求与修复背景；其中历史条目不替代当前工作区状态或发布记录。
- **部署运维**：参见下表中的部署、备份与安全指南。
- **专项实施**：前端生产改进以 [FOLLOW_UP.md](frontend-redesign/FOLLOW_UP.md) 为入口；脱敏当前进度以 [脱敏开发总览](脱敏开发/README.md) 为入口。

源码中已有实现、测试源码存在、CI 通过和线上验收是不同状态。以下规划不能作为发布或验收证明；保留的审计报告维持其形成时的上下文。

| 文档 | 说明 |
|------|------|
| [STANDALONE_DEPLOYMENT.md](STANDALONE_DEPLOYMENT.md) | 单机部署指南（二进制直接运行） |
| [SECURE_DEPLOYMENT.md](SECURE_DEPLOYMENT.md) | 安全部署指南（TLS、防火墙、硬化） |
| [BACKUP_RESTORE.md](BACKUP_RESTORE.md) | 备份与恢复操作手册 |
| [CHANNEL_PASSTHROUGH.md](CHANNEL_PASSTHROUGH.md) | 渠道 PassThrough 透传机制设计文档 |
| [DEVELOPMENT_routing.md](DEVELOPMENT_routing.md) | 请求路由与故障转移开发文档 |
| [STABILITY_UX_BASELINE.md](STABILITY_UX_BASELINE.md) | 稳定性与 UX 验收基线 |
| [FEATURES.md](FEATURES.md) | 功能需求清单（REQ/BUG 记录与设计意图） |
| [FREELLMAPI_PROVIDERS.md](FREELLMAPI_PROVIDERS.md) | FreeLLMAPI 免费 LLM 供应商清单（48 个供应商，含适配器与配额信息） |

## 脱敏开发（实现说明与后续规划）

请求脱敏核心功能已有实现（REQ-015）；规则增强与日志命中明细按各专项文档区分当前代码、未提交改动及待实施部分，不能将核心功能已实现理解为全部规划完成。

| 文档 | 说明 |
|------|------|
| [脱敏开发/README.md](脱敏开发/README.md) | 脱敏功能总览 |
| [脱敏开发/01-架构与插入点.md](脱敏开发/01-架构与插入点.md) | 架构设计与代码插入点 |
| [脱敏开发/02-规则引擎设计.md](脱敏开发/02-规则引擎设计.md) | 规则引擎与正则匹配设计 |
| [脱敏开发/03-流式还原设计.md](脱敏开发/03-流式还原设计.md) | 流式响应的脱敏与还原设计 |
| [脱敏开发/04-配置与前端.md](脱敏开发/04-配置与前端.md) | 配置 API 与前端管理页 |
| [脱敏开发/07-日志详情命中明细.md](脱敏开发/07-日志详情命中明细.md) | 日志命中明细（已实施：下发 label/占位符 + 有界原文片段） |

maskit 源码借鉴与思路参考（原 05/06）已从 docs/ 移除，历史参考见 git 历史。

## 前端重设计

| 文档 | 说明 |
|------|------|
| [frontend-redesign/ROUTE_TRANSITION_PERFORMANCE_PLAN.md](frontend-redesign/ROUTE_TRANSITION_PERFORMANCE_PLAN.md) | 页面切换与交互性能优化规划（skeleton、滚动复位、预加载、SSE 重渲染） |
| [frontend-redesign/FOLLOW_UP.md](frontend-redesign/FOLLOW_UP.md) | 生产前端美化后续计划、当前实现差异与验收清单 |

早期原型阶段的规划、设计、审计与原型快照已从 docs/ 移除，历史参考见 git 历史。

## 审计报告

| 文档 | 说明 |
|------|------|
| [audits/2026-09-18-full-audit.md](audits/2026-09-18-full-audit.md) | 2026-09-18/19 全面审计（基线实测 + 5 路深度审计 + 61 条历史问题核对） |
| [audits/AUDIT_ISSUES_2026-09-13.md](audits/AUDIT_ISSUES_2026-09-13.md) | 2026-09-13 静态审阅快照（历史问题清单来源；状态须对照最新全面审计复核） |

早前年份的归档审计报告已从 docs/ 移除，历史问题已并入 2026-09-18 全面审计第 8 节核对。
