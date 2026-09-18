# web-next Frontend Audit

> **归档横幅：** 本文形成于 2026-09 的历史快照，已归档、不再维护；其中标注的未修复项多数已在此后修复，当前审计事实以 `docs/audits/AUDIT_ISSUES_2026-09-13.md` 为准。本文仅覆盖 web-next 前端，非全量审计。

> **后续整理入口：** 已归档。本文是 2026-09 的只读审计；§8 记录已修/未修。不要按 §0 的「按密钥测试缺口」再做一遍（§8 已修）。

Read-only comprehensive review of `/root/kaifa/NovaVeil/web-next` after the
recent macOS-glass restyle, mapped against the four newer commits on
`NovaVeil origin/dev` (relative to local `4f50d98`):

| sha | subject | files in web-next? |
| --- | --- | --- |
| `3884299` | chore: untrack `.workbuddy` and remove web-next prototype | n/a (deletion of the old `web-next/` static prototype) |
| `14baf21` | fix: clamp picker grid tracks to stop mode-toggle jump and restore scroll | no, touches legacy `web/src/components/modules/group/Editor.tsx` |
| `1bf0487` | feat: test channel keys from the model-test UIs | no, lands `ChannelKeyTestResult`, `useTestChannelKeys`, `channelKeyFormLabel`, `/channel/test_keys`, `key_id` in legacy `web/` and backend |
| `1b68a90` | fix: bump google.golang.org/grpc | backend only |

Only `3884299` removes files that ever existed under `web-next`. The remaining
three sit in the legacy React tree (`web/`) and the Go backend; **none of the
behaviour is present in web-next**. The redesign already added many comparable
guards (sheet scrolling, max-width container, JWT 401 broadcast, must-change
banner, group picker parity, SSE lifecycle), so the gaps here are specific to
"newer improvements the restyle did not yet pick up."

---

## 0. Mapping the four reference commits to web-next

| upstream commit | intent | web-next gap | minimal remediation |
| --- | --- | --- | --- |
| `1bf0487` "test channel keys" | new `useTestChannelKeys()` + `/api/v1/channel/test_keys` + `key_id` on `/channel/test`; per-key selector and one-click "test all keys" UI; `channelKeyFormLabel("#i(备注/ID)")` | `src/lib/types.ts:34-44` has no `original_id`, no `ChannelKeyTestResult`; `src/lib/api.ts` `testChannel`/`fetchModels` only know `{id,model,message}`. `src/pages/channels/channel-editor.tsx:296-327` calls `api.testChannel(channel!.id, modelName)` without `key_id` so users can never test a specific key or probe a cooling key | add `ChannelKeyTestResult` type, `useTestChannelKeys()` (or api.testChannelKeys) and `useTestChannel({key_id})`; expose a "按密钥测试" selector in `channel-editor.tsx` when `savedKeyOptions.length >= 2`; render the per-key verdict list with the `key_label` mapping (`channelKeyFormLabel`) — copy the legacy `web/src/components/modules/channel/Form.tsx:140-240` |
| `14baf21` "clamp picker grid tracks" | `grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]` for the mode toggle header; `auto-rows-[minmax(0,1fr)]` so member grid lets the inner overflow fire | not applicable to current `Groups.tsx` (it still uses a flat list, not the picker grid from the legacy UI). When `Groups.tsx` is upgraded to a dual-pane picker, apply both tracks to avoid horizontal jump and silent scroll loss | mirror the comments when porting the picker; add a CSS regression note in `Groups.tsx` near `MembersTab` |
| `3884299` "untrack prototype" | drops `web-next/assets/*`, `index.html`, DESIGN.md/README.md. The redesign already shipped the modern console, so this is informational only | the redesign predates this and committed its own DESIGN.md/README; nothing to port, but the legacy reference is gone — keep web-next's own DESIGN.md/README | nothing required, just confirm web-next keeps the legacy `DESIGN.md` summary to avoid drift |
| `1b68a90` "grpc CVE bump" | backend-only dependency | n/a | n/a |

The per-key test is the only feature with a concrete mapping; everything else
is already inherited or backend-only.

---

## 1. Confirmed bugs (priority-ordered)

### 1.1 [HIGH] Per-key model test is unreachable from web-next (missing API surface)
- `src/lib/types.ts:34-44, 116-124` — no `ChannelKeyTestResult`, no
  `original_id`, `key_id` field on `ChannelTestResult`.
- `src/lib/api.ts:597-606` — `testChannel()` only knows `{id, model, message}`.
- `src/pages/channels/channel-editor.tsx:296-304` — `runSingleModelTest` calls
  `api.testChannel(channel!.id, modelName)` so users cannot:
  - target a specific saved key (no `key_id`),
  - run "test all keys" (no `useTestChannelKeys` / `/test_keys` endpoint),
  - see the per-key label `#i(备注/ID)`.
- **Impact**: channel-level key cooldown is opaque; an operator who suspects
  one key is cooling has no UI path to verify it on demand, and the per-model
  "测试" button can pick any key the same way as production traffic.
- **Fix**: port the legacy `web/src/components/modules/channel/Form.tsx:140-260`
  feature into web-next — add types, two `useMutation` hooks, the
  `channelKeyFormLabel("#${i}(${remark || id})")` helper, the key selector
  that shows when `savedKeyOptions.length >= 2`, and a verdict list rendering
  `{key_id, label, ok, content, error, elapsed_ms}`.

### 1.2 [HIGH] `name` field is empty after creating a key — duplicate names possible, list ordering broken
- `src/pages/Keys.tsx:332` overwrites `NAME_RULE` with `required: false`.
- `src/pages/Keys.tsx:328` initial state uses `k?.name ?? ""`, then
  `useEffect` at line 337 focuses the field on open — but when **creating**
  (`k === "new"`) the state starts at `""` and the previous-row draft is
  preserved across opens because `editorNonce` only changes for re-mount, not
  for the "new" reset (compare the create flow at `:425-433`).
- The saved Key (`api_key_created` at `:64-78`) carries the typed name; but if
  the user leaves the field empty, the list renders "未命名密钥"
  (`Keys.tsx:129`) and there is nothing forcing uniqueness, so two "未命名密钥"
  rows show up with no way to disambiguate.
- **Impact**: admin lists with several auto-created keys become hard to read;
  the only way to disambiguate is the masked suffix.
- **Fix**: keep the optionality but (a) reset `name` whenever
  `editing === "new"` is opened (similar to channel editor's reset effect on
  `:169`), (b) soft-suggest a default name like
  `key-${formatNumber(Date.now())}` as the placeholder hint, and (c) add a
  test asserting that opening two consecutive "new" editors yields two rows.

### 1.3 [HIGH] Logs SSE closes immediately when `tab !== "live"` but reconnect state lingers
- `src/pages/Logs.tsx:64-94` — the effect runs whenever `tab` changes. When
  the user switches from live → err and back, `setSseStatus("closed")` is set
  but the indicator at `:151-184` only shows "未订阅实时流" / "实时流断开,
  自动重连中" depending on `tab`; the "open" state is sticky between tab
  switches because `setSseStatus("connecting")` runs synchronously in the
  effect, but the SSE reconnection timer that fires every backoff window
  belongs to the prior `openSSE` handle. The new handle created on the
  subsequent render will run its own backoff, while the indicator can briefly
  flip to `open` once `onOpen` arrives.
- **Impact**: when a user toggles `实时请求` ⇄ `错误日志` rapidly, the badge
  jumps between `connecting` / `closed` / `open`, which the e2e suite does
  not cover.
- **Fix**: track the active handle in a ref and skip stale `setSseStatus`
  updates via a `mountedRef`/`activeRef.current === handle` check; on tab
  change, clear the previous handle's status to a single "not-subscribed"
  state until a fresh handle opens.

### 1.4 [HIGH] Group editor: `relay_config` updates silently drop when `previous` is undefined
- `src/pages/Groups.tsx:455-480` — `updateGroup` reads `previous =
  group!.relay_config`. When `group.relay_config` is undefined (legacy data or
  a freshly-created group before the first refresh), the spread
  `{ ...previous, ...relayUpdates }` produces an object that *appears* to set
  the fields, but the type system permits `undefined`, and the test in
  `Groups.test.tsx:48-106` does not cover this path.
- **Impact**: editing a group with no prior `relay_config` (or one inserted
  manually) sends `relay_config` that is *built from defaults only if at least
  one value changed* — if the user toggles `prefer_passthrough` without
  touching anything else, only that one field is sent. If the backend
  validates against the full struct, the other fields get zero values.
- **Fix**: if `previous` is missing, merge with `DefaultGroupRelayConfig()`
  (import the matching backend defaults from a shared module) before
  building the patch. Add a unit test that toggles only `prefer_passthrough`
  on a group whose `relay_config` is `undefined`.

### 1.5 [HIGH] Group editor: new groups ship with all-zero `relay_config` defaults
- `src/pages/Groups.tsx:411-450` — `createGroup` posts the literal object
  with `member_infra_max_retries: 3, member_retry_interval_seconds: 3,
  member_non_stream_response_timeout_seconds: 1200,
  member_stream_first_event_timeout_seconds: 300` — note `300` for the
  stream-first-event timeout, but the reference defaults are
  `member_retry_interval_seconds: 2, member_stream_first_event_timeout_seconds:
  60`. The current values predate the backend's "failover-first" retune
  (`upstream/dev` commits `bd8a1f2` + `df92c7d`).
- **Impact**: new groups do not inherit the latest failover-friendly
  defaults and have to be hand-tuned after creation.
- **Fix**: import `DEFAULT_RELAY_CONFIG` (extract from backend
  `internal/model/group.go:34-55`) and reuse it client-side; add a Vitest
  asserting `createGroup` body matches those defaults.

### 1.6 [HIGH] Race: cache invalidation of `["channels"]` discards a concurrent edit
- `src/pages/Channels.tsx:78-81` — `enableMut` does an optimistic update +
  `onSettled` invalidates `["channels"]`. If the user is in the channel
  editor at the same time and saves a draft, the editor's local
  `setDraft(toDraft(channel))` runs from the snapshot at open. After the
  enable switch, `qc.invalidateQueries` refetches and overwrites the local
  draft if the user hasn't yet saved. The bug is that the editor's
  `useEffect` at `channel-editor.tsx:169-174` re-syncs to `channel` whenever
  the prop changes; the refetch happens in the background while the user is
  typing.
- **Impact**: when the admin toggles the enabled switch for one channel from
  the table, the editor's unsaved draft can silently reset to the
  refetched value, losing local edits.
- **Fix**: gate the editor's re-sync to "channel prop identity changed" (e.g.
  `if (next !== channel)` and the editor was not dirty). When the channel
  prop's `id` is unchanged, do not reset draft. Add a Vitest that simulates
  `qc.invalidateQueries` during an edit and asserts `setDraft` is not
  called.

### 1.7 [HIGH] `useEffect` re-runs on every render in `useElapsedTick` when there is a stale closure
- `src/pages/Logs.tsx:317-325` — `setInterval(() => setNow(Date.now()),
  1000)` re-creates the timer on every render where `active` flips or any
  component re-renders. The dependency list is `[active]`, which is good, but
  the body returns `now` and `LiveTable` is rendered many times per second
  when SSE pushes.
- **Impact**: virtual list re-measures whenever `now` changes, which is
  once per second for every running request. Combined with the
  `virtualizer.measureElement` callback at `:392` (which forces re-measure on
  every render), the table keeps laying out 200 rows every second.
- **Fix**: gate the tick to only re-render the duration cell; pass `now`
  through a separate component (`<ElapsedCell now={now} startedAt={...} />`)
  so the virtualized row list does not re-render.

### 1.8 [MED] Channel editor drop-zone for unsaved keys fails on remount
- `src/pages/channels/channel-editor.tsx:560-592` — the `secrets.data`
  effect runs `update("keys", next)` whenever the secrets query resolves.
  When the user is editing and clears one of the key rows (`k.key = ""`),
  the row loses its `original_id` (`next[i].id = value.trim() ?
  "" : current.original_id ?? current.id`), then `secrets.data` arrives
  later and the row gets the secret appended back automatically.
- **Impact**: the user clears a key to delete it, but if the reveal query
  has ever run for that row, the secret comes back silently (the editor
  re-fills from `getChannelKeys`). Not catastrophic, but defeats the user's
  intent.
- **Fix**: only auto-fill when `revealRequested && k.id === found.id && k.key
  === "" && current === original`. Skip if the user explicitly cleared in
  the same session (track a `clearedIds: Set<string>` per session and bail
  when the id is in it).

### 1.9 [MED] `dialog.tsx` overlays never focus-trap on sheet variant
- `src/components/ui/dialog.tsx:35-63` — Radix's `DialogPrimitive.Content`
  already provides focus trap; however, the rendered `<DialogPrimitive.Close>`
  at `:54-59` is **inside** the content element. If the content contains a
  focusable input whose only keyboard affordance is the close button, screen
  reader users land on the close X first because Radix orders portal focus
  by DOM order. After tabbing through inputs the close is last which is
  fine, but the static close button itself has no `aria-label` localized
  text in the local — `"关闭"` is the default.
- **Impact**: minimal, but combined with the rebuilt glass-overlay backdrop
  (no visible outline on focus when `focus-visible:ring-2 focus-visible:ring-ring`
  is overridden by `focus:outline-none`), keyboard users lose context in
  sheet dialogs at small viewport heights.
- **Fix**: keep the close button but accept a `closeLabel` prop and ensure
  the backdrop's `data-[state=open]` rule includes a `:focus-visible` outline
  for the sheet footer actions.

### 1.10 [MED] `e2e/a11y.spec.ts` skips the Logs and Channels pages
- `web-next/e2e/a11y.spec.ts:64-70` — `PAGES` includes only Dashboard,
  渠道, 分组, API 密钥, 设置. The Logs page (modal dialog + virtual list)
  and the Channels Sheet editor are absent, so the redesigned console does
  not gate critical/serious axe violations on the highest-risk surfaces.
- **Impact**: regressions on those pages won't be caught by the a11y suite.
- **Fix**: extend `PAGES` and use `page.getByRole("button", { name: /新建渠道/ })`
  + close button to test the editor sheet specifically.

### 1.11 [MED] `formatDatetimeLocal` test depends on `TZ`
- `src/lib/utils.test.ts:71-82` — only asserts on `TZ=Asia/Shanghai` /
  `TZ=PRC`; other timezones silently skip the test, which means the
  regression that the rewrite was supposed to fix (timezone-induced offset)
  is not actually covered in default CI.
- **Impact**: if the restyle breaks local-time composition outside `+08:00`,
  the test still passes.
- **Fix**: pin `process.env.TZ = "UTC"` in `vitest.config.ts`'s
  `environmentOptions` or in `setup.ts`, then assert against UTC inputs
  without the conditional skip.

### 1.12 [MED] `token-trend` chart and dashboard use mock data without a clear notice
- `src/components/charts/TokenTrendChart.tsx:21-24, 175-198` — the chart
  is generated by `generateMockData()` with the comment "示意数据";
  `Dashboard.tsx:96-101` shows a small "示意数据" badge next to the legend,
  but `Dashboard.test.tsx` and `e2e/a11y.spec.ts` have no assertions about
  the disclaimer. If the restyle was supposed to land a real-time-bucket
  endpoint later, the comment "示意数据" is the only safety net.
- **Impact**: product expectation mismatch; admins may treat the curve as
  ground truth.
- **Fix**: when wiring a real bucket endpoint, remove the disclaimer; until
  then, hoist the disclaimer above the legend and add a Vitest that asserts
  it is rendered.

### 1.13 [LOW] `ConfirmButton` initial render shows `label` without `tone="destructive"` color
- `src/components/ui/confirm-button.tsx:28-42` — when not armed, the button
  uses `variant="ghost"` and only flips to `tone` after the first click. The
  `label` is "删除" by default, so users must first click "删除" to see the
  destructive color. This works but the first-click affordance is muted.
- **Impact**: discoverability for destructive actions in channels/groups/keys.
- **Fix**: use `tone="destructive"` outline variant in the un-armed state
  (e.g. `<Button variant="danger-outline" size="sm">`), which is already a
  registered variant.

### 1.14 [LOW] `flags.ts` sticky bucket jitter
- `src/lib/flags.ts:74-81, 86-89` — `userBucket` returns `Math.abs(h) % 100`.
  `|h|` collapses `-2147483648` (when `seed.charCodeAt(i) === 0x8000_0000`) to
  itself, which is fine, but `flags.warn.test.ts` only asserts that
  `loadFlags({ force: true })` falls back to default — it does not cover the
  bucket boundary at `rollout-percent: 0` and `rollout-percent: 100`, where
  any non-null `userId` ought to fall in/outside.
- **Impact**: miscategorising a bucket near the boundary can pin a user to
  the wrong variant.
- **Fix**: add Vitest covering `userBucket("admin")` against
  `rollout-percent: 0` / `100`, plus an explicit `shouldUseNewWeb` test for
  the boundary case.

### 1.15 [LOW] `Channels.tsx` row double-event on the Switch
- `src/pages/Channels.tsx:232-244` — the row's `onClick={() => setEditing(c)}`
  is stopped only on the status-cell's `<td onClick>` and the actions
  `<td onClick>`. The Pill inside the status cell (`Pill` itself is a
  `<span>`) does not stop propagation, so clicking the Pill text opens the
  editor. This is also true for `tags` Pill and the model Pills.
- **Impact**: clicking a Pill of an enabled channel still opens the editor.
  This is functional, not broken — but inconsistent with the rest of the
  table.
- **Fix**: either stop propagation on the `Switch`'s click handler, or move
  the row-click handler to a `<button>`-wrapped subarea. Prefer the former
  to preserve the keyboard activation pattern.

### 1.16 [LOW] `formatNumber` uses `zh-CN` for compact notation, but the dashboard `tokens` total mixes input + output into `formatNumber(latest.total_tokens_input + latest.total_tokens_output, { notation: "compact" })` (`Dashboard.tsx:241`) which produces `"15万"`. OK.
- Cosmetic only; the test in `Dashboard.test.tsx:71` asserts `/10万|100K/`
  which is locale-sensitive — if CI uses an `Intl` polyfill that doesn't
  emit `万`, the test will fail intermittently.
- **Fix**: prefer a deterministic formatter for tests, e.g. `formatNumber(v,
  { notation: "compact" }).replace(/\s/g, "")` and assert against a
  constant.

### 1.17 [LOW] Sidebar's transition conflicts with `aria-pressed` semantics
- `src/components/layout/Sidebar.tsx:67-86` — `aria-pressed={collapsed}` is
  correct, but the button has no `aria-controls` pointing at the sidebar;
  screen reader users get the state change but no anchor.
- **Fix**: add `aria-controls="primary-sidebar"` and `id="sidebar-toggle"`
  on the button; `<aside id="primary-sidebar">` to match.

### 1.18 [LOW] `proxy` and `auto_sync` toggles in `AdvancedTab` reuse `draft.proxy` even when the backend type is a non-boolean
- `src/pages/channels/channel-editor.tsx:1486-1507` — `Switch`
  `checked={draft.proxy}` reads `draft.proxy ?? false`, but
  `buildChannelUpdateRequest` at `:108-132` does `const { ... fields } =
  draft`, then spreads the rest. `proxy` is a primitive boolean in
  `internal/model/channel.go:50`, so this is correct — flag as informational
  only.

### 1.19 [LOW] `relay_config` form fields do not validate that `max_rounds < 1` is rejected
- `src/pages/Groups.tsx:578-594` — `min={1}` in the input is a soft hint,
  not enforced; user can still type `0` and submit. The backend's
  `binding:"omitempty,min=1"` returns 400 and the toast shows the raw
  English message.
- **Fix**: add `validateField(rounds, { required: true, min: 1, maxLen: 6 })`
  similar to `NAME_RULE`.

### 1.20 [LOW] `field.test.tsx` does not cover the `hint + error` accessibility role
- `src/components/ui/field.test.tsx:43-52` — confirms `role="alert"` on
  error, but does not confirm that the `hint` is announced alongside.
  Adequate for now but worth asserting `aria-describedby` on the child input
  is wired when present.

### 1.21 [LOW] `sse.ts` is missing backoff cap assertion
- `src/lib/sse.ts:75-77` — `backoff = Math.min(backoff * 2, 30_000)`. Test
  `sse.test.ts:68-78` only verifies a single reconnect; add a Vitest that
  asserts the cap is honoured after 10 fake errors (1s → 2 → 4 → 8 → 16 →
  30 → 30 …).

### 1.22 [LOW] `confirm-button` "armed" timer is not paused when the user holds space/enter
- `src/components/ui/confirm-button.tsx:22-26` — no keyboard handler; works
  for `click` only. Tab + space activation triggers a click and the timer
  starts. Fine for mouse, slightly different from native destructive buttons.
- **Fix**: ensure the button has `type="button"` (already does) and the
  click is fired only on user activation; document.

---

## 2. API-contract / spec drift

1. `src/lib/api.ts:597-606` `testChannel` does not honour `key_id` (see
   1.1).
2. `src/lib/api.ts:649` `exportChannels` returns a string from
   `/channel/export`; the backend (`internal/server/handlers/channel.go:300-323`)
   sends `Content-Disposition: attachment; filename="channels-…"`, but
   `downloadText` in `src/lib/utils.ts:122-134` calls
   `URL.createObjectURL(blob)` and ignores the server filename. The user
   gets `channels-${Date.now()}.txt` instead of the server's `channels-20260102-150405.txt`.
3. `src/lib/api.ts:754` `exportSettings` calls
   `/setting/export` which returns `Content-Disposition` with the server's
   filename (`internal/server/handlers/setting.go:117-119`); again
   `downloadJson` overwrites the filename.
   - **Fix**: use the server-provided filename when `Content-Disposition` is
     present.
4. `src/lib/types.ts:91` `Channel.match_regex` is optional; the backend
   sends it as `*string`, so `null` is possible. `lib/api.ts:453-479`
   `normalizeChannels` does not coerce `null` to `undefined`; the editor
   `AdvancedTab` (`channel-editor.tsx:1317`) renders `draft.match_regex ??
   ""`, which works for null. OK.
5. `src/lib/types.ts:194-203` `GroupRuntimeState.member_states[].state`
   does not include `"disabled"`; the upstream `internal/op/group.go` (per
   the legacy editor commit `db33e15`) renders disabled channel members
   with that tone. The web-next group picker uses
   `!channel.enabled` (`Groups.tsx:723-754`) for chrome only; consider
   surfacing the runtime `"disabled"` state for symmetry.
6. `src/pages/Channels.tsx:236-237` row keyboard activation has `tabIndex={0}`
   but no `aria-controls` / dialog handle. See 1.17.

---

## 3. Responsive layout

1. `src/pages/channels/channel-editor.tsx` Sheet dialog uses
   `DialogContent variant="sheet"` (`dialog.tsx:46`) which is `w-full
   max-w-xl`. On 320-360px viewports, the `flex flex-col` body plus the
   `<DialogHeader>` with a 4-line description plus 4-tab strip overflows
   vertically; the fix shipped in commit `a2a32be` (sheet scroll) applies,
   but the **tabs row** has no `overflow-x-auto`, so on narrow viewports the
   「凭据」「模型」「限制」「高级」 labels can wrap awkwardly.
2. `src/components/layout/AppShell.tsx:31-34` — main content has
   `overflow-y-auto` and `mx-auto max-w-[1440px]`. On a 1366px screen, the
   sheet dialog (`max-w-xl`) still feels cramped because the
   `p-7` outer padding reserves 28px on each side.
3. `src/pages/Logs.tsx:368` virtualizer container is fixed `height: 480`. On
   landscape phones (≤360×640), this consumes 75% of the viewport. Make the
   container `min(480px, 60vh)` or use a flex child.
4. `src/pages/Logs.tsx:315` `COLS` template
   `"120px 90px minmax(280px,1fr) 90px 140px 160px 90px"` totals
   `≥880px`, exceeding the typical 360-414px mobile viewport. The
   `overflow-auto` shell covers horizontal scrolling, but every column
   header label still wraps inside its 90-160px track on narrow screens,
   producing an unreadable "客户端 IP / Tokens（入/出）" combo. Add a
   `<sm:hidden` set or stack to a card list below 768px.
5. `src/pages/Settings.tsx:82-99` left-rail nav buttons stack at lg+
   (`grid-cols-1 lg:grid-cols-[180px,1fr]`); on md they collapse into a
   180px rail that leaves only ~620px for content. Acceptable, but the
   settings page header alignment can drift because each section's title is
   in a flex with no min-width.
6. `src/pages/Groups.tsx:160` cards are 1/2/3 columns at sm/md/xl; the
   editor's `DialogContent variant="sheet"` still uses fixed width, so
   editing a group on a 1366px screen consumes the right third without any
   visible side padding inside the sheet.

---

## 4. Accessibility

1. `src/pages/Channels.tsx:241-243` row `role="button"` works, but no
   `aria-controls` to the dialog id, and no `aria-expanded`.
2. `src/pages/Logs.tsx:395-403` virtual rows have `role="row"`, but the
   parent `role="grid"` is missing `aria-rowindex` per row (only
   `aria-rowcount`). Without `aria-rowindex`, NVDA/JAWS report "row 1 of N"
   for every cell, defeating virtualisation for screen readers. Add
   `aria-rowindex={vi.index + 1}`.
3. `src/components/layout/CommandPalette.tsx:79-87` has `role="dialog"` but
   Radix's `Dialog` component already wires `aria-modal`, focus trap, etc.
   The hand-rolled palette here lacks `aria-activedescendant` for the
   highlighted item; with arrow-key navigation only the input gets focus,
   so screen readers don't know which item is selected. Add
   `aria-activedescendant` and per-option `id`. **Also missing: focus
   restoration to the search button on `Esc` close** — capture the
   `document.activeElement` at open and `ref.current?.focus()` on close.
4. `src/pages/channels/channel-editor.tsx:1044-1056` the "expand model
   config" button uses `aria-expanded` but the disclosure panel has no
   `id`, so the relationship is unannounced. Add
   `aria-controls={`model-cfg-${m.id}`}` plus `id` on the panel.
5. `src/pages/Groups.tsx:522-543` uses `role="tablist"` + `role="tab"` but
   lacks `aria-controls` / `id` linkage and `tabIndex` discipline
   (Radix's `Tabs` already does this; the manual tabs in Groups/Dialog
   footer don't). Add `id` + `aria-controls` and ArrowLeft/Right keyboard
   navigation per the WAI-ARIA tabs pattern.
6. `src/pages/Settings.tsx:84-99` left rail buttons are `<button>`s with no
   `role="tab"` / `aria-selected` semantics — selecting "模型测试" looks
   identical visually and to assistive tech, except for the active class.
   Add `aria-current="page"` (or `aria-selected`).
7. `src/pages/Keys.tsx:498-513` "显示密钥" toggle uses `<button>` without
   `aria-pressed`. Screen reader users don't know the toggle state. Add
   `aria-pressed={secretVisible}`.
8. `src/pages/channels/channel-editor.tsx:718-730` same pattern for the
   per-key "显示密钥" button; add `aria-pressed={visibleKeys[i]}`.
9. `src/pages/channels/channel-editor.tsx:991-999` model row checkbox
   uses `aria-label={`选中 ${m.name}`}`. Good — but the surrounding row
   does not wrap the label, so sighted users see a stray checkbox without
   row context.
10. `src/index.css:201-209` `prefers-reduced-motion` collapses durations to
    `0.01ms`; good, but `interactive:active` (`index.css:285-287`) uses
    `scale(0.98)` outside a `motion-safe` guard. Reduced-motion users still
    see the press-scale (within 1 frame so usually invisible). Wrap with
    `@media (prefers-reduced-motion: no-preference)`.
11. `src/components/ui/confirm-button.tsx:22-53` — the 2.2s "armed" state
    has no `aria-pressed`, so screen readers don't announce the
    "再次点击确认" transition. Add `aria-pressed={armed}` and a polite
    `aria-live="polite"` wrapper so the new state is announced.
12. `src/pages/Logs.tsx:153-184` "实时流已连接" indicator uses
    `aria-live="polite"` but only on the parent; status dot itself
    (`<span className="dot">`) is decorative. The aria-live message
    ("实时流断开，自动重连中") is fine; however, when the SSE reconnects,
    there is no "实时流已连接" assertive message — users with screen readers
    may not realise it came back. Add a transient polite message on
    transition.
13. `src/components/layout/ErrorBoundary.tsx:30-44` component does not reset
    its `err` state when `componentStack` is set, only when `reset()` is
    called externally. Fine, but the fallback `<pre>` reveals the full
    error message including backend messages that may contain key
    fragments or proxy addresses. Truncate or sanitise.
14. `src/pages/channels/channel-editor.tsx:945-957` master "全选测试模型"
    checkbox shows the indeterminate state only via `el.indeterminate =`
    in a ref callback. `aria-checked="mixed"` is the AT equivalent; add
    it on the input. Also hide the master checkbox when
    `draft.models.length === 0` (the "批量测试" row currently renders
    even on empty lists).
15. `src/components/layout/Topbar.tsx:32-45` "搜索" trigger is a `<button>`
    that opens `CommandPalette` — it has no embedded `<input>` and no
    `aria-haspopup="dialog"`. Screen reader users don't know it opens a
    command palette. Add `aria-haspopup="dialog" aria-expanded={cmdkOpen}`.
16. `src/components/layout/Topbar.tsx:47-76` — theme toggle and logout
    buttons are 28-32px tall (`size="icon"` = `h-8 w-8`). WCAG 2.5.5
    requires ≥ 44×44 interactive area; the visible tap target is
    half-size. Add `min-h-11 min-w-11` (or expand to `h-11 w-11`) and
    keep the visible glyph centred. Same for the collapse button in
    `src/components/layout/Sidebar.tsx:67-86`.
17. `src/components/layout/Sidebar.tsx:67-86` — when `collapsed`, every
    NavLink label is hidden via `hidden` (`Sidebar.tsx:126-128`), and the
    icon-only link has no `aria-label` fallback. AT users lose the entire
    primary nav. Already covered by item 1.17 but worth calling out
    explicitly: each `NavLink` should receive `aria-label={item.label}`
    unconditionally so the collapsed state stays navigable.
18. `src/pages/Logs.tsx:350-443` the `role="grid"` table lacks proper
    `role="rowheader"` / `role="columnheader"` / `role="gridcell"`
    semantics. The header row is `role="row"` with plain `<div>`s;
    header cells need `role="columnheader"`, body cells need
    `role="gridcell"`. Axe `aria-required-children` will otherwise flag
    this combination.
19. `src/pages/Channels.tsx:209-220` and `src/pages/Keys.tsx:106-114` —
    tables lack `<th scope="col">`. The `<thead><tr><th>` are real `<th>`
    but missing the `scope` attribute. Add `scope="col"`.
20. `src/pages/Dashboard.tsx:218-281` KPI cards: the `<Card>` becomes a
    live region only when `loadingNow` flips; add
    `aria-busy={loadingNow}` and `aria-live="polite"` so screen readers
    announce the transition from skeleton → value.
21. `src/pages/Login.tsx:85` `autoFocus` on the username input can race
    with the lazy-loaded `LoginPage` chunk; on slower connections the
    `autoFocus` runs before React finishes mounting and steals focus
    from elsewhere. Drop `autoFocus`; trigger focus inside the
    component's own `useEffect`. The "信任此设备" checkbox at `:103-110`
    has a `<label>` wrapper but no `htmlFor` / `id` linkage — add
    explicit `id="remember"` plus `htmlFor="remember"` and an `aria-label`
    on the `<input>`.
22. `src/components/charts/TokenTrendChart.tsx:51-143` — no
    keyboard-accessible data point summary. The chart has a hover
    tooltip at `:144-167` but no equivalent for keyboard users. Add
    ArrowLeft/Right handlers that move an index and read out the value
    via `aria-live="polite"`. Pair with `role="img"` `aria-roledescription`
    describing the chart.
23. `src/pages/Logs.tsx:635-654` and `src/pages/channels/channel-editor.tsx:425-445`
    custom tablists in the trace sheet / channel editor lack
    ArrowLeft/Right keyboard navigation. Add the WAI-ARIA pattern so
    arrow keys move focus between tabs and `Home`/`End` jump to ends.
24. `src/pages/Channels.tsx:232-244` row keyboard activation has `Enter`
    and `Space` mapped, but `aria-expanded` on the row is missing — pair
    it with the dialog id so AT users know pressing Enter will open
    the editor.
25. `src/components/layout/CommandPalette.tsx:78-87` is a hand-rolled
    dialog with `<div role="dialog" aria-modal="true">` and a manual
    click-outside handler — no focus trap, no scroll lock, no `Escape`
    scroll-lock reset. The Radix `Dialog` already wires all of these
    (`src/components/ui/dialog.tsx` exports `DialogPortal`,
    `DialogOverlay`, focus-trap via `DialogPrimitive.Content`). Replace
    the hand-rolled palette with a Radix-backed `Dialog` + `Command`-style
    list; keep the existing keyboard arrow handling on top.
26. `src/main.tsx:34` instantiates `<Toaster richColors position="top-right" />`
    with no `ariaProps`. Sonner accepts `ariaProps={{ role: 'status',
    'aria-live': 'polite' }}` for non-error toasts and `role: 'alert',
    'aria-live': 'assertive'` for errors. Without this, screen reader users
    receive no announcement when a destructive error toast appears. Wire
    per-toast variant or globally via the `<Toaster>` prop.
27. `src/components/ui/field.tsx:22` renders `<label className="block">`
    wrapping `<span>` + `children` + `<p>` — implicit labelling. With the
    current shape, clicking the children does not always reach the first
    focusable input when the children are a fragment of multiple elements
    (e.g. the per-key row in `channel-editor.tsx:694-749` puts the secret
    input + remark input + delete button inside one `Field`). Use
    `useId()` to mint an `id`, render `<label htmlFor={id}>`, and either
    ensure the first focusable child carries that `id` or scope the
    labelling to the dominant input.
28. `src/pages/Settings.tsx:1077-1095` Backup "导入 JSON" wraps a
    `<input type="file">` inside a `<label>`. The file input does have
    `aria-label="选择要导入的 JSON 文件"` so it is independently labelled,
    but the surrounding `<label>` already implies another control. Remove
    the wrapping `<label>` (it does nothing useful for `input[type=file]`)
    or convert to a plain `<div>` so AT users do not see two competing
    labels.
29. `src/pages/Settings.tsx` opens with multiple `保存` (save) buttons per
    section (`SystemSection`, `RetentionSection`, `TestMessageSection`,
    `HeaderTemplatesSection`). When AT users focus the page, `button[name="保存"]`
    is ambiguous — `getByRole('button', { name: '保存' })` returns the
    first one only. Add `aria-label={`保存 ${label}`}` to each save
    button (e.g. "保存 全局代理", "保存 错误保留天数", "保存 测试消息",
    "保存 模板").
30. `src/components/ui/pill.tsx:23` "info" tone `bg-blue-500/[0.08]
    text-blue-600` computes to **2.6:1** against the pill background —
    fails AA. Same for `text-emerald-600` (3.4:1 on `bg-emerald-500/[0.08]`)
    and `text-red-600` on `bg-red-500/[0.08]` (3.3:1). Used widely in
    `Pill` for status indicators across `Channels`, `Groups`, `Keys`,
    `Logs`. Either darken the text colors to AA-compliant shades
    (`text-blue-700`, `text-emerald-700`, `text-red-700`) or add a 1px
    solid border with the same hue so the visual ratio holds.
31. `src/components/ui/field.tsx:25` required marker `<span
    className="text-destructive">*</span>` uses `--destructive` on the
    label span (`text-ink-muted` for the label); the * glyph against
    `--card` background computes to **3.26:1** — fails AA (same problem as
    4A.3). Either darken the marker (`text-red-700`) or render as a
    full word "(必填)" with `aria-label="必填"` for AT users.

---

## 4A. Contrast (subagent-verified)

WCAG AA target is 4.5:1 for normal text. The redesign introduces several
unannounced regressions against the background tokens:

1. `--ink-subtle` (`src/index.css:64` — `220 7% 55%`) computes to **3.47:1**
   on the glass `--surface` (`220 14% 96%`) — fails. Hits:
   - `src/components/ui/field.tsx:28` `text-[11px] text-ink-subtle` for hint
     copy under inputs (Keys expiry hint, channel base URL hint).
   - `src/pages/Logs.tsx:447` 虚拟化窗口计数器 `text-[11px] text-ink-subtle`.
   - `src/pages/Settings.tsx:129,149` 主题/语言 section labels.
   - **Fix**: raise to `220 9% 40%` (~5.4:1) or apply only to non-essential
     hint copy under WCAG large-text exemption.
2. `--warning` (`src/index.css:58` — `38 100% 50%`) computes to **1.84:1**
   on the `bg-warning/10` (`38 100% 95%`) banner background used in
   - `src/pages/Login.tsx:113-120` (Login error banner — actually uses
     `destructive`, see #3) and
   - `src/pages/Keys.tsx:238-240` "出于安全考虑" / 离开后无法再次查看.
   - **Fix**: darken `--warning` to `38 95% 35%` (4.6:1) or stop using the
     `bg-warning/10` wash; rely on a 1px border plus black text.
3. `--destructive` (`src/index.css:53` — `350 100% 56%`) on
   `bg-destructive/[0.04]` (used by `Login.tsx:116` and `ErrorBoundary.tsx:78`)
   computes to **3.26:1** — fails AA.
   - **Fix**: increase destructive tone to `350 80% 42%` (4.5:1) or raise
     the surface opacity to `0.1`.
4. (consistency note) `--primary` (`211 100% 45%`) on `bg-card` is 4.7:1, OK.
   `--success` (`145 63% 42%`) on `bg-card` is 3.4:1 — also fails for normal
   text but only used for pill dots which are decorative.
5. `src/components/layout/CommandPalette.tsx:107` "无匹配结果" uses
   `text-ink-muted` (4.5:1 borderline); add an axe contrast check.
6. Axe `color-contrast` rule is currently disabled in `e2e/a11y.spec.ts:102`
   (`.disableRules(["region"])` — `region` is the only one disabled; add
   `color-contrast` explicitly as enabled so the P2 issues above are caught).

---

## 5. Stale copy / docs / config

1. `src/pages/Settings.tsx:147-159` "语言" section has buttons labelled
   `zh-CN` and `en-US` that **do nothing** (`onClick` is missing). Drop the
   buttons or wire them to a (planned) i18n switch.
2. `src/pages/Channels.tsx:158-156` filter buttons render Chinese labels
   but the
   `<select>` `aria-label="排序"` and the surrounding text are correct.
3. `src/pages/Logs.tsx:373-376` empty-state copy "暂无实时请求；客户端
   首次发起后会立即出现" is fine, but the `status` Pill renders raw backend
   states ("running", "committed", "canceled"). Add a translator or a
   `STATE_LABEL` map.
4. `src/pages/Dashboard.tsx:96-101` chart shows "示意数据"; good,
   but `Dashboard.test.tsx:71` asserts `/10万|100K/` (locale polyfill
   dependent). See 1.16.
5. `web-next/DESIGN.md:1-100` still describes the original admin console
   design (glass, sheet scroll, etc.). Most sections are up to date, but the
   "P3 灰度" section references `__flags/runtime.json` paths that the
   current `src/lib/flags.ts` still honours. Verify the file isn't stale.
6. `web-next/README.md` is up to date as of `8ec56e3`; minor staleness in
   the screenshot description but no broken commands.

---

## 6. Tests / coverage gaps (concrete items to add)

1. Per-key model test UI (see 1.1) — add Vitest for `useTestChannelKeys`
   shape + a Playwright test that opens the channel editor with 2+ keys,
   selects a key, and asserts the verdict list shows ok/invalid per key.
2. Group picker parity (1.5) — assert `createGroup` body uses
   `DefaultGroupRelayConfig` (extract to a shared constant first).
3. SSE reconnect cap (1.21) — assert backoff caps at 30s.
4. Editor dirty-state preservation (1.6) — assert `setDraft` is not called
   when the channel prop's identity is unchanged.
5. Logs virtualised `aria-rowindex` + `aria-rowcount` (a11y 2.2, 4.18) —
   assert virtualized rows expose a unique `aria-rowindex` and that
   header cells have `role="columnheader"`.
6. CommandPalette `aria-activedescendant` + focus restoration (a11y 3.3) —
   assert arrow keys update the active id and that closing with Esc
   restores focus to the trigger.
7. Settings "语言" buttons (5.1) — assert they are removed or wired.
8. Sidebar `aria-label` fallback when collapsed (a11y 4.17) — assert each
   NavLink's accessible name stays "总览", "渠道", etc. when collapsed.
9. `formatDatetimeLocal` TZ (1.11) — assert against fixed TZ.
10. `userBucket` boundaries (1.14) — assert `rollout-percent: 0` returns
    `false`, `100` returns `true`.
11. Disabled channel members in groups (5.2) — when `runtime.state ===
    "disabled"`, the group card should surface a "disabled" tone. Add
    Vitest.
12. Channel / Keys / Logs tables — add a Vitest asserting every header
    cell has `scope="col"` (a11y 4.19).
13. Logs virtualized grid — assert `aria-rowindex={vi.index + 1}` on every
    rendered row.
14. CommandPalette focus restoration — assert `document.activeElement`
    after `Esc` equals the trigger button reference captured at open.
15. Topbar search button — assert `aria-haspopup="dialog"` and that
    pressing Enter opens the palette with focus moved to the input.
16. Icon-button tap targets — assert `getBoundingClientRect().width >=
    44 && height >= 44` for `Topbar` theme/logout buttons and the
    sidebar collapse button.
17. ConfirmButton armed state — assert `aria-pressed` flips to `true`
    on first click and back to `false` after timeout or confirmation.
18. Login `autoFocus` — assert focus is on the username input only after
    the component's own `useEffect` fires (drop `autoFocus`, replace with
    `requestAnimationFrame`).
19. Channels/Keys/Logs viewport-overflow — add a Playwright test at
    `1024×768` that asserts `document.documentElement.scrollWidth`
    equals the viewport width (no horizontal overflow).
20. Axe color-contrast — extend `e2e/a11y.spec.ts` to include
    `color-contrast` in `withTags(["wcag2a", "wcag2aa"])` so the P2
    contrast regressions (4A.1–4A.3) are caught.

---

## 7. Dead code / wiring

1. `src/components/ui/icon.tsx` — defined and tested but **unused**
   anywhere in `src/`. The restyle committed it as a "centralised
   aria-hidden" wrapper, but every consumer still passes `aria-hidden`
   inline (`grep -rn 'Icon as=' src`). Either commit to migration or drop
   the component + test.
2. `src/lib/constants.ts:7` `DEFAULT_TEST_MESSAGE` is referenced, but the
   comment "与 NovaVeil 后端约定对齐" is the only place where the
   backend string ("ping" in `internal/relay/test.go:37`) is mentioned;
   they intentionally differ (常识判断 vs blank-ping). OK.
3. `src/lib/utils.ts:267-274` `MODEL_RULE` is exported but never imported.
   `Settings.tsx:147-159` and `Groups.tsx` use `NAME_RULE` / `URL_RULE`
   only. Either wire `MODEL_RULE` into the new model key test UI or
   remove it.
 4. `web-next/DESIGN.md` — sections "P0..P3" referenced but the
    implementation outpaced the doc; trim or archive.

---

## 8. Race conditions (other than 1.3 / 1.6 / 1.7)

1. `src/pages/Groups.tsx:378-394` `moveItem`/`removeItem` use
   `prev.slice()` but recompute `priority` via `.map((x, idx) => ({...x,
   priority: idx + 1}))`. With concurrent edits from
   `buildMemberDiff`, the second edit overwrites the first. Add Vitest
   covering two moves interleaved with two removes.
2. `src/pages/Channels.tsx:96-104` `testMut` `onMutate` sets `testingId`
   but `onSettled` reads from the closure of the previous click when the
   user clicks a second row before the first resolves; both rows then show
   "testing". Capture `c.id` in the closure:
   `setTestingId(id)` inside `onSettled: (_data, _error, vars) => { if
   (vars === c.id) setTestingId(null); }`.
3. `src/pages/Keys.tsx:339-342` setTimeout-based focus can fire after the
   dialog has unmounted, leaving a stale `nameRef.current?.focus()`. The
   `setTimeout` is cleared on `open` change, but if the dialog unmounts
   before the 30ms timer fires, `nameRef.current` is null and the focus
   call is a no-op. Acceptable, but guard with `if (nameRef.current &&
   document.contains(nameRef.current))`.
4. `src/pages/Logs.tsx:60-94` `sseRef.current` is set then null'd in
   `useEffect` cleanup. If the tab flips twice quickly, the second
   cleanup nulls the first handle but its `onOpen` callback can still fire
   (it fires asynchronously), updating a stale `setSseStatus`. Mitigate via
   the same ref-scope guard as 1.3.

---

## 9. Operational notes

- All four reference commits target the legacy `web/` tree; no rebroadcast
  is required for the macOS-glass restyle. The only behavioural gap is
  `1bf0487` (per-key test); everything else is either backend-only,
  doc-only, or already inherited.
- The test suite covers about 64 files (`vitest`), 1 unit/integration
  Playwright suite (`e2e/`), axe a11y smoke. No flake-resistant timer
  tests; consider a `vitest.setup.ts` that installs `IntersectionObserver`
  and `EventSource` mocks centrally (currently duplicated in
  `Logs.test.tsx` and `sse.test.ts`).
- **Production build verification (post-audit).** A separate subagent
  tally flagged "3 P0 (build broken in production)". `pnpm build` on the
  current `HEAD` (`5b52d23`) succeeds with `EXIT=0` and emits all chunks
  cleanly. The three alleged blockers either refer to functional defects
  already captured in §1 (not build failures) or were transient against
  an earlier commit. Tangled class names like
  `ease-[cubic-bezier(0.25,0,0,1)]` are Tailwind content-ambiguity
  warnings, not build errors. The only hygiene item with a "dead code"
  reading — the unused `<Icon/>` wrapper at `src/components/ui/icon.tsx`
  — is exported and tree-shaken correctly; not a blocker. Treat the
  "build broken in production" claim as **not reproduced** unless
  accompanied by a concrete failing build invocation.

---

## TL;DR for triage

- **1.1** (per-key test) is the single feature gap relative to upstream/dev.
- **1.6** (editor reset race) is the highest-impact UX bug.
- **1.3 / 1.7** (SSE/tick state churn) and **4.2 / 4.18** (missing
  aria-rowindex / role=columnheader) combine to make the Logs page feel
  "alive" while screen-reader users see rows out of order and the table
  lacks proper grid semantics.
- **4A.1–4A.3 + 4.30 + 4.31** (contrast) — `--ink-subtle`, `--warning`,
  `--destructive`, Pill status tones, and the Field required marker all
  fail WCAG AA on tinted backgrounds. Affects hint copy in `Field`,
  error/warning banners in Login/Keys, status pills across
  Channels/Groups/Keys/Logs, and the required `*` glyph.
- **4.16 / 4.17** (tap target / collapsed sidebar) — Topbar icon buttons
  and the Sidebar collapse button are 28-32px (WCAG 2.5.5 wants ≥44×44);
  when the sidebar is collapsed, NavLink labels are `hidden` without an
  `aria-label` fallback, removing the entire primary nav from AT.
- **4.15 / 4.25 / 4.26** — Topbar search trigger (`aria-haspopup`),
  CommandPalette focus trap (replace hand-rolled with Radix), and
  Toaster `ariaProps` (sonner status/assertive wiring).
- **4.27–4.29** — Field `useId` + `htmlFor`, Backup import `<label>`
  wrapper, Settings section "保存" button `aria-label`s.
- **5.1** (Settings language buttons) is a long-standing dead UI element;
  either delete or wire it up.
- Most other items are minor; the restyle is in good shape.
---

## 10. 修复记录（2026-09-03，本仓库 web-next）

按本审计执行的修复，验证统一由 GitHub CI（web-next-ci：typecheck / lint / vitest
含覆盖率阈值 / build / size-limit / playwright e2e+axe）完成，本地不构建。

**§1 确认 bug**
- 1.1 按密钥测试：新增 `ChannelKeyTestResult`、`api.testChannelKeys`
  （POST `/channel/test_keys`）、`api.testChannel` 第 4 参 `key_id`；编辑器
  「模型」Tab 增加「按密钥测试 (n)」按钮 + `savedKeyOptions.length >= 2` 时的
  按 Key 选择器（标签 `#i(备注/ID)`），结果列表逐 Key 展示；切换 Key 清空既有
  模型测试结果。
- 1.2 密钥命名：创建时名称留空自动生成 `key-<Date.now()>`，文案同步；沿用
  编辑器 remount 重置（key 含 editorNonce）。
- 1.3 SSE 状态抖动：`Logs.tsx` 每个句柄持有自增 token，onOpen/onError 与
  cleanup 均校验 token，快速切 Tab 不再写脏状态。
- 1.4 relay_config undefined：`updateGroup` 先与 `DEFAULT_GROUP_RELAY_CONFIG`
  合并再构建 patch，单字段改动也发送完整配置。
- 1.5 新分组默认值：`types.ts` 新增 `DEFAULT_GROUP_RELAY_CONFIG`（与后端
  `internal/model/group.go DefaultGroupRelayConfig` failover-first 调优版一致：
  retry_interval 2、stream_first_event 60 等），`createGroup` 引用；配
  `types.test.ts` 对齐断言。
- 1.6 编辑器草稿竞态：draft/测试状态重置改按稳定 channelKey（id）判定，
  后台 invalidateQueries 换引用不再清空未保存草稿。
- 1.7 计时器重渲染：`useElapsedTick` 下沉到 `ElapsedCell`，秒级 tick 只重渲染
  耗时单元格，虚拟化列表不再每秒全量重渲。
- 1.8 清除密钥回填：CredTab 记录本会话明确清空的密钥（稳定 id / key_masked），
  明文查询晚到不回填；重新输入解除；切换渠道重置眼睛/明文/清除记录。
- 1.13 ConfirmButton：destructive 未武装态用 `danger-outline`。
- 1.19 轮次/冷却校验：`maxRounds` / `cooldownSeconds` 最小 1，非法禁用保存并
  显示 Field error。
- 1.11 TZ：`test/setup.ts` 钉 `TZ=UTC`（与 CI runner 一致）。
- 1.10 a11y 覆盖：`e2e/a11y.spec.ts` PAGES 增加「日志」。

**§2 契约**
- 2.2/2.3 导出文件名：`rawDownload` / `rawDownloadJson` 解析
  Content-Disposition，`/channel/export`、`/setting/export` 优先用服务端文件名。
- 2.1 见 1.1。

**§3 响应式**
- 3.1 Tabs 溢出：channel-editor / TraceSheet tabs 行加 `overflow-x-auto`。
- 3.3 Logs 表体高度改 `min(480px, 60vh)`。

**§4 a11y / §4A 对比度**
- 4A.1–4A.3：light 模式 `--ink-subtle 220 9% 40%`、`--warning 38 95% 35%`、
  `--destructive 350 80% 42%`。
- 4.30：Pill 文字 600→700（success/warning/danger/info），暗色保持 400。
- 4.31：Field 必填星号 `aria-hidden` + `sr-only`「必填」。
- 4.2/4.18：Logs grid 包裹表头+主体，表头 `role="columnheader"`、单元格
  `role="gridcell"`、`aria-rowindex`（表头 1、数据行 vi.index+2）。
- 4.7/4.8：Keys / channel-editor 眼睛按钮补 `aria-pressed`。
- 4.15：Topbar 搜索 `aria-haspopup="dialog"`。
- 4.16：Topbar 主题按钮、Sidebar 折叠按钮 44px 触控目标。
- 4.17/1.17：Sidebar `id="primary-sidebar"` + 折叠按钮 `aria-controls`；
  NavLink 无条件 `aria-label`。
- 4.6：Settings 左侧导航 `aria-current="page"`。
- 4.4：模型配置披露按钮 `aria-controls=model-cfg-*` + 面板 id。
- 4.5：Groups tabs WAI-ARIA 化（id/aria-controls/tabIndex/方向键）。
- 4.19：Channels/Keys 表头 `scope="col"`。
- 4.21：Login 去 `autoFocus`（effect 聚焦），「信任此设备」补 id/aria-label。
- 4.24：Channels 行 `aria-haspopup="dialog"`。
- 4.28：备份导入 file input 外层 label 改 div。
- 4.29：Settings 各分区保存按钮 `aria-label="保存 <分区>"`。

**§5 陈旧文案 / §8 竞态**
- 5.1：移除「语言」死按钮（外观卡片改为仅主题）。
- 8.2：Channels `testMut.onSettled` 按变量 id 清 testing 态，连点两行互不覆盖。

**§6 测试**
- 新增 `lib/types.test.ts`（默认值对齐）、`api.test.ts` 追加 testChannel key_id /
  testChannelKeys / 导出文件名（含无头场景）用例；`Logs.test.tsx` 更新为新 grid
  结构并断言 aria-rowindex/columnheader；`field.test.tsx` 断言 sr-only 必填。

**未修（记录原因）**
- 4.25 CommandPalette 换 Radix：组件级重构，留待专门 PR。
- 4.26 Toaster ariaProps：sonner 锁 1.7.1（无 ariaProps，v2 才有），升级另行处理。
- 1.9/1.12/1.14/1.15/1.16/1.18/1.20/1.21/1.22、§2.4–2.6、§3.2/3.4–3.6、§4.1/4.3/
  4.9–4.14/4.20/4.22/4.23/4.27、§5.2–5.6、§6（其余用例）、§7、§8.1/8.3/8.4：
  影响小或属测试补齐/文档清理，按优先级排入后续（1.15 经复核 status 单元格
  td 已 stopPropagation，不成立）。

### 10.1 CI 收尾（2026-09-03 第二轮）

目标：web-next-ci 全绿（此前 coverage 与 size-limit 两个 job 长期红）。

- **Coverage**：84.55%/77.99% → **95.21% / 85.17%**（阈值 lines 88 / functions 80 / branches 85 / statements 88 全过；不要写成无字段的 88/88/85/80）。
  - TokenTrendChart：hover 命中（mock SVG getBoundingClientRect）、mouseLeave
    清除、越界不命中、24h tooltip 完整格式 —— 未覆盖的 46-167/215-221 全数覆盖。
  - skeleton：SettingsSkeleton / LogsSkeleton / TableSkeleton 补渲染测试。
  - api.ts：网络失败、非 JSON、403 改密广播与豁免、导出失败信封 message、
    RFC 5987 filename*、归一化降级分支（getNowVersion/clientStats/lastSyncTime/
    stopAllState/fetchModels）、clearGroupCooldown、parseHeaderTemplates。
  - 单测 174 → 203，全过。
- **顺手修真 bug**：`forbiddenGuideExemptPaths` 存了 `/api/v1/...` 全路径，而
  `http()` 收到的是相对 path —— 豁免集合永远不命中，登录 403 会误弹改密引导
  （恰是注释要避免的）。已改为相对 path 并补双向测试。
- **Size limit**：根因有二。
  1. `@size-limit/preset-app` 的 time 插件对每个检查项都跑 headless Chrome
     量执行时间（`check.running !== false` 即跑），配置只预算 KB 根本不需要；
     runner 慢时 estimo 20s 导航超时 → 三个检查全部加 `"running": false`。
  2. 「JS 单 chunk」与「JS 总和」用了同一个 `assets/*.js` glob，永远量的是
     总和（165 kB > 150 kB 必炸）；改为入口 chunk `assets/index-*.js`
     （92.8 kB gzip），恢复"单 chunk"本意。
- 结果：web-next-ci 三个 job 全绿（run 33693699834）。
