import { test, expect, type Page, type Route } from "@playwright/test";

/**
 * E2E 冒烟 —— 五大核心页面
 *  策略：Playwright 拦截 /api/* 返回固定数据，避免依赖真实后端
 *  覆盖：路由可达 + 关键元素渲染 + 主题切换
 *
 * 鉴权处理：所有已登录用例都先到 /（让 AuthProvider 完成 /user/status），
 * 再通过点击侧栏 NavLink 导航到目标页 —— 比直接 page.goto 更接近真实用户路径。
 */

const FLAG_OK = {
  "new-web": true,
  "rollout-percent": 100,
  "sticky-bucket": true,
  "ab-mode": "auto",
  "legacy-path": "/legacy",
};

const USER_OK = { must_change_password: false };

function jsonResponse(route: Route, body: unknown) {
  return route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ code: 200, message: "success", data: body }),
  });
}

async function mockApi(page: Page) {
  // 兜底：任何未覆盖的 /api/v1/* 返回空
  await page.route("**/api/v1/**", (route) => jsonResponse(route, null));

  // flags
  await page.route("**/__flags/*.json", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(FLAG_OK),
    }),
  );
  // 认证
  await page.route("**/api/v1/user/status", (route) => jsonResponse(route, USER_OK));
  await page.route("**/api/v1/user/login", (route) => jsonResponse(route, USER_OK));
  // 仪表盘
  await page.route("**/api/v1/stats/now-version", (route) =>
    jsonResponse(route, {
      version: "0.13.0",
      client_ip_count: 42,
      total_requests: 1024,
      error_count: 0,
      total_tokens_input: 100_000,
      total_tokens_output: 50_000,
      tokens_by_model: [
        { model: "gpt-4o", total_tokens: 80_000 },
        { model: "claude-3.5", total_tokens: 50_000 },
      ],
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

/** 经侧栏 NavLink 导航；登录态由探活 mock 提供 */
async function navTo(page: Page, label: string) {
  await page.goto("/");
  // 等侧栏出现（已登录态）
  await page.waitForSelector('[data-testid="sidebar"]', { timeout: 10_000 });
  // 点侧栏的 nav item
  await page.getByRole("link", { name: label }).first().click();
  // 等网络空闲
  await page.waitForLoadState("networkidle");
}

test.describe("E2E 冒烟", () => {
  test.beforeEach(async ({ page }) => {
    await mockApi(page);
  });

  test("首页直达 dashboard", async ({ page }) => {
    await page.goto("/");
    await page.waitForSelector('[data-testid="sidebar"]', { timeout: 10_000 });
    await expect(page.getByRole("heading", { level: 1 })).toContainText("总览");
    await expect(page.getByText("总请求").first()).toBeVisible();
    await expect(page.getByText("客户端 IP").first()).toBeVisible();
    await expect(page.getByText("错误数").first()).toBeVisible();
    await expect(page.getByText("Token 用量").first()).toBeVisible();
  });

  test("渠道页：空态 + 新建按钮可见", async ({ page }) => {
    await navTo(page, "渠道");
    await expect(page.getByRole("heading", { level: 1 })).toContainText("渠道");
    await expect(page.getByRole("button", { name: /新建渠道/ })).toBeVisible();
    await expect(page.getByText(/还没有渠道/)).toBeVisible();
  });

  test("分组页：空态", async ({ page }) => {
    await navTo(page, "分组");
    await expect(page.getByRole("heading", { level: 1 })).toContainText("分组");
    await expect(page.getByText(/还没有分组/)).toBeVisible();
  });

  test("密钥页：空态", async ({ page }) => {
    await navTo(page, "API 密钥");
    await expect(page.getByRole("heading", { level: 1 })).toContainText(/API 密钥/);
    await expect(page.getByText(/还没有密钥/)).toBeVisible();
  });

  test("日志页：Tab 切换", async ({ page }) => {
    await navTo(page, "日志");
    await expect(page.getByRole("heading", { level: 1 })).toContainText("日志");
    await expect(page.getByText(/实时请求/).first()).toBeVisible();
    await page.getByRole("button", { name: "错误日志" }).click();
    await expect(page.getByText(/类别/).first()).toBeVisible();
  });

  test("设置页：12 个分区可点击", async ({ page }) => {
    await navTo(page, "设置");
    for (const name of [
      "外观",
      "账户",
      "系统",
      "代理池",
      "Header 模板",
      "对话留存",
      "错误日志保留",
      "用量保留",
      "模型测试",
      "上游模型同步",
      "备份",
      "关于",
    ]) {
      await expect(page.getByRole("button", { name }).first()).toBeVisible();
    }
  });

  test("主题切换：light → dark", async ({ page }) => {
    await page.goto("/");
    await page.waitForSelector('[data-testid="sidebar"]', { timeout: 10_000 });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await page.getByRole("button", { name: "切换主题" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  });

  test("侧边栏折叠：data-collapsed 切换 + localStorage 持久化", async ({ page }) => {
    await page.goto("/");
    await page.waitForSelector('[data-testid="sidebar"]', { timeout: 10_000 });
    const sb = page.getByTestId("sidebar");
    await expect(sb).toHaveAttribute("data-collapsed", "false");
    await page.getByTestId("collapse-btn").click();
    await expect(sb).toHaveAttribute("data-collapsed", "true");
    const stored = await page.evaluate(() =>
      localStorage.getItem("nv-sidebar-collapsed"),
    );
    expect(stored).toBe("1");
  });

  test("登录页：401 错误显示", async ({ page }) => {
    await page.route("**/api/v1/user/status", (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ code: 401, message: "unauthorized" }),
      }),
    );
    await page.goto("/login");
    await expect(page.getByText("NovaVeil")).toBeVisible();
    await expect(page.getByText("LLM API 网关控制台")).toBeVisible();
    await page.route("**/api/v1/user/login", (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ code: 401, message: "unauthorized" }),
      }),
    );
    await page.getByLabel("用户名").fill("admin");
    await page.getByLabel("密码").fill("wrong");
    await page.getByRole("button", { name: /登录/ }).click();
    await expect(page.getByText(/用户名或密码错误/)).toBeVisible();
  });

  test("回退页：new-web=false → 看到 legacy 入口", async ({ page }) => {
    await page.route("**/__flags/runtime.json", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...FLAG_OK,
          "new-web": false,
          "rollout-percent": 0,
        }),
      }),
    );
    await page.goto("/dashboard");
    await expect(page.getByText(/经典版控制台/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: /进入旧版控制台/ }),
    ).toBeVisible();
  });
});
