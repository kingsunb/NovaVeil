# Agent Note: legacy-path 链接协议白名单

Status: implemented

## 问题

审计 FE-06/F-L5 确认：`App.tsx` 的 rollback 通知把 `flags["legacy-path"]` 直接塞进 `<a href>`，该值来自
runtime.json/localStorage override，只要配成 `javascript:alert(1)` 就会生成可点击的 XSS 链接。

## 决定

- `src/lib/flags.ts` 新增 `safeLegacyHref(value)`：只允许 `http://`、`https://`、`mailto:` 或以
  `/`、`./`、`../` 开头的相对路径；显式拒绝 `//` 协议相对地址，其它值一律回退 `/legacy`。
- `App.tsx` 的旧版入口链接从 `href={flags["legacy-path"]}` 改为 `href={safeLegacyHref(flags["legacy-path"])}`。

## 备选方案

- **在 flags 加载阶段校验并丢弃非法值**：更早隔离，但会丢失默认回退语义；`safeLegacyHref` 保持原始值、
  只在渲染处安全化，便于 audit 与复用。
- **只允许 http/https**：会破坏 `/legacy` 这类既有内部相对路径配置；选择同时保留 mailto 与相对路径。
- **使用 `URL` parser 并校验 protocol 列表**：更通用，但需要额外处理相对路径与空值，4 行字符串决策已够。

## 后果

- **收益**：`legacy-path` 无论来自 runtime.json 还是 localStorage override，都无法注入 `javascript:` 等
  危险 scheme。
- **代价与已知上限**：合法但非白名单路径（如自定义 `web+` deep link）会被回退为 `/legacy`。

## 验证

- `src/lib/flags.test.ts` 的 `safeLegacyHref` describe：允许 http/https/mailto 与相对路径；拒绝
  `javascript:`、`//evil.example.com`、`data:` 与空串。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
