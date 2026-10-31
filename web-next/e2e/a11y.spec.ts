import { test, expect, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

/**
 * axe-core a11y 扫描 —— P4.2 关键页 a11y 验收
 *  阈值：critical/serious 违规为 0；moderate/minor 仅记录不阻断
 *  用 Playwright 的 @axe-core/playwright 包装
 */

const FLAG_OK = {
  "new-web": true,
  "rollout-percent": 100,
  "sticky-bucket": true,
  "ab-mode": "auto",
  "legacy-path": "/legacy",
};

function jsonResponse(route: Route, body: unknown) {
  return route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ code: 200, message: "success", data: body }),
  });
}

async function mockApi(page: Page) {
  await page.route("**/api/v1/**", (route) => jsonResponse(route, null));
  await page.route("**/__flags/*.json", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(FLAG_OK),
    }),
  );
  await page.route("**/api/v1/user/status", (route) =>
    jsonResponse(route, { must_change_password: false }),
  );
  await page.route("**/api/v1/stats/now-version", (route) =>
    jsonResponse(route, {
      version: "0.13.0",
      client_ip_count: 42,
      total_requests: 1024,
      error_count: 0,
      total_tokens_input: 0,
      total_tokens_output: 0,
      tokens_by_model: [],
    }),
  );
  // 版本看门狗轮询的轻量端点；commit 与前端注入值不可判定时按 unknown 静默
  await page.route("**/api/v1/stats/build-info", (route) =>
    jsonResponse(route, { version: "0.13.0", commit: "e2e", build_time: "" }),
  );
  await page.route("**/api/v1/log/errors**", (route) => jsonResponse(route, []));
  await page.route("**/api/v1/channel/list", (route) => jsonResponse(route, []));
  await page.route("**/api/v1/group/list", (route) => jsonResponse(route, []));
  await page.route("**/api/v1/apikey/list", (route) => jsonResponse(route, []));
  await page.route("**/api/v1/channel/last-sync-time", (route) =>
    jsonResponse(route, { last_sync_at: "" }),
  );
  await page.route("**/api/v1/log/stop-all-state", (route) =>
    jsonResponse(route, { is_stopped: false }),
  );
  await page.route("**/api/v1/setting/get*", (route) => jsonResponse(route, null));
  await page.route("**/api/v1/setting/list", (route) => jsonResponse(route, []));
}


const PAGES = [
  { name: "Dashboard", navLabel: "" },
  { name: "渠道", navLabel: "渠道" },
  { name: "自定义模型", navLabel: "自定义模型" },
  { name: "分组", navLabel: "分组" },
  { name: "API 密钥", navLabel: "API 密钥" },
  { name: "日志", navLabel: "日志" },
  { name: "设置", navLabel: "设置" },
];

test.describe("axe a11y 扫描", () => {
  test.beforeEach(async ({ page }) => {
    await mockApi(page);
  });

  for (const p of PAGES) {
    for (const theme of ["light", "dark"] as const) {
      test(`${p.name} (${theme}) 无 critical/serious 违规`, async ({ page }) => {
        // 用直接 goto + 侧栏导航（确保主题一致）
        // 登录态由探活 mock 给出，仅预置主题
        await page.addInitScript((t) => {
          localStorage.setItem("nv-theme", t as string);
        }, theme);
        await page.goto("/");
        await page.waitForSelector('[data-testid="sidebar"]', { timeout: 10_000 });
        // 等 ThemeProvider 同步
        await page.waitForFunction(
          (t) => document.documentElement.dataset.theme === t,
          theme,
        );
        if (p.navLabel) {
          await page.getByRole("link", { name: p.navLabel }).first().click();
          await page.waitForLoadState("networkidle");
        }
        const result = await new AxeBuilder({ page })
          .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
          .disableRules(["region"])
          .analyze();
        const critical = result.violations.filter(
          (v) => v.impact === "critical" || v.impact === "serious",
        );
        if (critical.length) {
          console.log(
            `[${p.name}/${theme}] violations:`,
            critical
              .map((v) => `${v.id}(${v.impact}): ${v.help}`)
              .join("\n"),
          );
        }
        expect(
          critical,
          `页面 ${p.name}（${theme}） 存在 critical/serious a11y 违规`,
        ).toEqual([]);
      });
    }
  }
});
