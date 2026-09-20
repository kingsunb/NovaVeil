# Agent Note: 修改密码增加「确认新密码」字段

Status: implemented

## 问题

审计 OLD-28 确认：`ChangePasswordForm` 只有「当前密码」与「新密码」两个输入，用户一旦输错新密码就会
把登录口令改成一个无法回忆的值，且缺少二次确认这一常见安全控制。

## 决定

- `ChangePasswordForm.tsx` 新增 `确认新密码` 输入框，`autoComplete="new-password"`；
- 当新密码达到 8 位且确认框有值但不一致时展示 `role="alert"` 错误文案，并把两个 input 标为 invalid；
- 提交按钮只在 `当前密码非空 && 新密码 ≥ 8 位 && 确认一致` 时可用；成功后清空三个字段并刷新登录态。

## 备选方案

- **后端强制二次确认字段**：协议改动大，且 UI 层校验已能防止误输入；后端 schema 不扩展维持现有契约。
- **只在失焦时提示不一致**：实现更重，提升有限；输入期间实时提示错误更传统。
- **复用 Field 组件**：当前表单是紧凑网格布局，保持现有结构即可。

## 后果

- **收益**：防手滑改错口令；提交前一致性校验为标准行为，测试已覆盖。
- **代价与已知上限**：若未来接入密码强度规则，需把校验函数再抽一层。

## 验证

- `src/components/auth/ChangePasswordForm.test.tsx`：不一致时禁用提交并出现 alert；一致后提交并刷新登录
  态，成功后三个字段清空。
- `src/App.test.tsx` 的修改密码成功路径已补填确认框。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
