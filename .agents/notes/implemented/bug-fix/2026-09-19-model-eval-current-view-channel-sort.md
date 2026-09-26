# Agent Note: 模型评估与分组编辑的渠道展示排序

Status: implemented

## 问题

模型评估页「当前评估」视图和分组编辑器都按渠道展示模型。直接沿用 `/channel/list` 后端返回顺序（按 id）不能反映用户设置的渠道优先级，跨页对照容易产生困惑。渠道管理页的默认「自定义排序」将非内置渠道排在前面，再按 `sort` 降序排列（数值大 = 优先级高 = 靠前，同值按名称兜底）。

## 决定

`web-next/src/pages/ModelEval.tsx` 的 `allTargets` 在 `flatMap` 展开渠道模型前，先按渠道优先级和名称排序：

```ts
(b.sort ?? 0) - (a.sort ?? 0) || a.name.localeCompare(b.name)
```

`sort` 越大越靠上，同值按渠道名。该排序作用于「当前评估」（以及共享该目标列表的 `EvalDetail` 目标查找）。

`web-next/src/pages/Groups.tsx` 的 `ChannelModelPicker` 对过滤后的渠道列表采用渠道页 `custom` 的完整排序规则：

```ts
Number(a.builtin) - Number(b.builtin) ||
(b.sort ?? 0) - (a.sort ?? 0) || a.name.localeCompare(b.name)
```

新建和编辑分组共用此选择器；搜索和清空搜索保持同一顺序，停用渠道保留在原有可选范围内。排序只作用于选择器的渠道展示，不改 React Query 缓存中的原始数组，也不调整分组成员的 `priority`、自动匹配追加顺序或故障转移顺序。

## 备选方案

- **在 `EvalSelection` 分组后再排序**：需要给 `EvalTarget` 增加 `sort` 字段或额外传入渠道排序信息，侵入面更大，未选。
- **后端 `/channel/list` 直接返回排序后的列表**：会让所有消费方受同一顺序约束，同时改变其他页面的契约；渠道排序本质是前端展示策略，未选。
- **同时按渠道页规则过滤 `type === "custom"` 渠道**：本次需求只对齐「排序」，未要求排除自定义固定回复渠道；扩大改动会引入额外的行为变化，保留现状（评估页不排除 custom 渠道）以控制范围。

## 后果

- 收益：评估页反映渠道自定义优先级；分组编辑器的渠道展示顺序与渠道管理页默认视图一致，搜索后也便于按相同位置查找渠道。
- 边界：排序规则内联在 `Channels.tsx`、`Groups.tsx` 和 `ModelEval.tsx`，未来调整需检查三处（尚未抽公共函数）。模型评估的排序不区分 `builtin`；评估页和分组选择器都不排除 `type === "custom"` 渠道，与渠道管理页的可选范围存在差异。

## 验证

- `web-next/src/pages/ModelEval.test.tsx` 覆盖三条渠道 `sort` 分别为 10 / 30 / 20 且以 id 顺序乱序返回时，「当前评估」渲染顺序为 30 → 20 → 10。
- `web-next/src/pages/Groups.test.tsx` 覆盖新建和编辑分组的渠道排序：非内置优先、`sort` 降序、同值按名称、停用渠道保留，以及搜索过滤和清空搜索后的顺序。
- 本次修改按用户要求仅作源码与 Git 差异审阅，未执行编译、测试、应用运行或笔记门禁。
