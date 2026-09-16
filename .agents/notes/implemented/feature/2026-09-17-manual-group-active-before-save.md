# Agent Note: 分组手动模式——新增成员即可指定为「当前」，保存才生效

Status: implemented

## 问题

分组编辑器（`web-next/src/pages/Groups.tsx` 的 `GroupEditor`）在 manual 模式下，每个成员行有一个「当前」单选，用来指定分组的当前承载成员（`active_item_id`）。该单选按后端数字 `id` 跟踪选中态：`checked={it.id > 0 && activeItemId === it.id}`、`disabled={it.id === 0}`、`onChange` 仅在 `it.id > 0` 时生效。

后果是：**新加入但尚未保存的成员 `id === 0`，单选被禁用，无法指定为「当前」**。管理员必须先保存分组让成员拿到后端 id，再重新打开编辑器才能选中它。这与「添加后即可配置，保存才落库」的直觉相悖——成员列表里能看到新增项，却点不动它的「当前」。

根因不是 UI 漏写，而是选中标识选错了维度：`activeItemId` 是后端 id，对草稿里 `id=0` 的新成员天然无法表达。

## 决定

把 manual 模式的「当前」选中态从数字 `activeItemId` 改为按 `client_uid`（字符串）跟踪。`client_uid` 对已保存成员（`saved:<id>`）与新增成员（`new:<n>` / `auto:<n>`）都稳定，由 `normalizeGroupOne` / `nextDraftItemUid` 保证唯一，因此新增成员一经添加即可被选中。

落点全在 `GroupEditor` 内：

- 状态 `activeItemId: number` → `activeUid: string | null`。加载已有分组时，把后端 `active_item_id` 映射到对应成员的 `client_uid`；找不到（被删/数据不一致）为 `null`。
- 「当前」单选：`checked={activeUid === it.client_uid}`，移除 `disabled={it.id === 0}`，`onChange` 直接 `setActiveUid(it.client_uid)`。选中项是未保存成员（`id === 0`）时，标签后追加「·保存后生效」提示。
- 保存时把 `activeUid` 解析回后端 id 再调 `api.setActiveGroupItem`，分三种情况：
  - 选中已保存成员（`id > 0`）→ 直接 `setActive(group.id, id)`；
  - 选中未保存新成员（`id === 0`）→ `create`/`update` 之后该成员获得 id，按 `priority`（草稿内唯一、与提交一致）在响应里定位出 id 再 `setActive`；定位失败回退第一条已保存成员；
  - 未选（含原 active 成员被删除）→ 回退第一条已保存成员，否则清空。

新增分组路径同样尊重用户选择：`createGroup` 后按选中成员的 `priority` 在响应里解析 id 调 `setActive`，不再无脑取 `created.items[0]`（仅在未选/解析失败时才回退到第一条，保留「manual 分组至少有一个可路由成员」的原保证）。

「保存才生效」由流程本身保证：选中只改本地 `activeUid`，不发任何请求；`setActiveGroupItem` 永远在 `create`/`update` 成员落库之后才调用。

## 备选方案

- **保存时把所有新成员先落库，再让用户选** — 即维持现状要求先保存。最强论据是零代码改动、选中态始终是真实 id；否掉因为它把「配置」与「落库」强行串行，用户在同一个编辑会话里加成员后必须中断去保存才能继续配置当前成员，体验割裂，正是本次要修的痛点。
- **双轨：`activeItemId` 保留给已保存成员，另加一个临时 uid 给新成员** — 最强论据是改动局部、不动现有 id 路径；否掉因为两套标识并存要在 checked/onChange/save 三处都做「id 还是 uid」的分支判断，状态机更碎、更易漏边角（如选中项从新成员变成已保存成员）。统一用 `client_uid` 单一维度更简单，且 `client_uid` 本就是为「草稿稳定标识」引入的（见 `normalizeGroupOne` 注释）。
- **用 `priority` 直接作为选中标识** — 最强论据是 priority 草稿内唯一、保存时也用于解析 id，省一个字段；否掉因为 priority 在 add/remove/重排时会**被动重算**（`next.map((x, idx) => ({ ...x, priority: idx + 1 }))`），把它当选中键会把「重排」误判成「换当前成员」。`client_uid` 不随重排变，语义正确。

## 后果

- **收益**：manual 分组里新增成员后立即可指定为「当前」，配置与落库解耦，符合「添加即可配、保存才生效」的直觉；选中态单一维度（`client_uid`）覆盖草稿与已保存成员，删掉了 `id === 0` 的禁用分支。
- **代价与已知上限**：未保存成员被选为「当前」时，其 `setActive` 依赖 `create`/`update` 响应里能按 `priority` 反查到新 id；这依赖后端在响应中回传全部成员且 `priority` 与提交一致（现状如此，`normalizeGroupOne` 不改 priority）。重访信号：若后端某天对 `items_to_add` 重排 priority 或响应不回传新成员，未保存选中项会落空回退到第一条——此时需改用更稳的反查键（如后端回传临时 client_uid）。emergency 兜底成员（`relay_config.emergency_item_id`）仍要求已保存成员，因其 id 随 `relay_config` 在同一次 `update` 里提交，无法延后解析，不在本次改动范围。

## 验证

`web-next/src/pages/Groups.test.tsx` 新增「手动模式当前成员：新增成员即可指定，保存才生效」用例组：断言新成员单选未禁用、选中后保存前无 `active` 请求、保存后 `setActive` 用解析出的新 id（既有分组用 update 后的 id=2；新建分组用 create 后的第二个成员 id=12 而非默认第一个 id=11）。`pnpm typecheck`、`pnpm vitest run`（502 项全绿）通过。
