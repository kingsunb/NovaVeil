# Agent Note: legacy-path 链接协议白名单

Status: implemented

## 问题

审计 FE-06/F-L5 确认：`App.tsx` 的 rollback 通知把 `flags["legacy-path"]` 直接塞进 `<a href>`，该值来自
runtime.json/localStorage override，只要配成 `javascript:alert(1)` 就会生成可点击的 XSS 链接。

## 决定

- `src/lib/flags.ts` 的 `safeLegacyHref(value)` 用 URL 解析放行 `http`/`https`/`mailto`，以及解析后仍同源的 `/`、`./`、`../` 路径。反斜杠、空白、协议相对 `//` 和 `///host` 回退 `/legacy`。判定见 [web mutation 缓存与导航防护](./2026-09-22-web-mutation-cache-and-nav-guards.md)。
- `App.tsx` 的旧版入口链接使用 `href={safeLegacyHref(flags["legacy-path"])}`，不直接写入 flags 原值。

## 备选方案

- **在 flags 加载阶段校验并丢弃非法值**：更早隔离，但会丢失默认回退语义；`safeLegacyHref` 保持原始值、
  只在渲染处安全化，便于 audit 与复用。
- **只允许 http/https**：会破坏 `/legacy` 这类既有内部相对路径配置；选择同时保留 mailto 与相对路径。
- **只用前缀判断、不用 URL 解析**：最初认为四行字符串决策已够。`/\evil.com` 与 `///host` 仍以 `/` 开头，浏览器或 URL 解析器会把它们当成跨源地址，所以现行实现改为 URL 解析。

## 后果

- **收益**：`legacy-path` 无论来自 runtime.json 还是 localStorage override，都无法注入 `javascript:`、反斜杠伪装的协议相对地址或 `///host`。
- **代价与已知上限**：合法但非白名单路径（如自定义 `web+` deep link）会被回退为 `/legacy`。

## 验证

- `src/lib/flags.test.ts` 的 `safeLegacyHref`：允许 http/https/mailto 与同源相对路径；拒绝 `javascript:`、`//`、`///host`、反斜杠、空白、`data:` 与空串。
