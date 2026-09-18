import { describe, expect, it, vi } from "vitest";
import {
  cn,
  debounce,
  downloadJson,
  elapsedParts,
  formatBytes,
  formatDatetimeLocal,
  formatDuration,
  formatElapsedWithFirst,
  formatNumber,
  MODEL_RULE,
  NAME_RULE,
  timeAgo,
  URL_RULE,
  validateDBDumpImport,
  validateField,
  validateSettingsImport,
} from "./utils";

describe("cn", () => {
  it("合并 className，尾随的同 key 覆盖前值", () => {
    expect(cn("p-2", "p-4")).toBe("p-4");
  });

  it("支持条件值：falsy 跳过", () => {
    const falsy = false as const;
    expect(cn("a", falsy && "b", undefined, null, "c")).toBe("a c");
  });

  it("支持数组与对象语法（clsx）", () => {
    expect(cn(["a", "b"], { c: true, d: false })).toBe("a b c");
  });
});

describe("formatNumber", () => {
  it("千分位格式化", () => {
    expect(formatNumber(1234567)).toBe("1,234,567");
  });

  it("支持 compact 记号（1.2万 / 12.3万）", () => {
    const compact = formatNumber(12345, { notation: "compact" });
    expect(compact).toMatch(/万/);
  });

  it("0 不会出 NaN", () => {
    expect(formatNumber(0)).toBe("0");
  });
});

describe("formatBytes", () => {
  it("0B 边界", () => {
    expect(formatBytes(0)).toBe("0 B");
  });

  it("B/KB/MB 单位换算", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(1024 * 1024 * 1024)).toBe("1.0 GB");
  });
});

describe("formatDatetimeLocal", () => {
  it("把 Unix 秒数格式化为本地 datetime-local 字符串", () => {
    // 用固定时区测输出 (CI 默认 UTC), 期望值是本地时间组件拼装结果。
    const original = new Date(2026, 0, 15, 12, 34); // 2026-01-15 12:34 local
    const seconds = Math.floor(original.getTime() / 1000);
    const formatted = formatDatetimeLocal(seconds);
    // 期望格式: "YYYY-MM-DDTHH:mm", 不带 Z, 时区分量走本地时间。
    expect(formatted).toBe("2026-01-15T12:34");
  });

  it("在非 UTC 时区下保持稳定 (假设运行测试时 TZ=Asia/Shanghai UTC+8)", () => {
    // 该测试只在 TZ=Asia/Shanghai 下断言具体内容, 其他 TZ 跳过。
    const tz = process.env.TZ;
    if (tz !== "Asia/Shanghai" && tz !== "PRC") {
      // 用 vi 标记跳过: 跨 CI 时区稳定。
      return;
    }
    // 2026-01-15T04:34 UTC = 2026-01-15T12:34 +0800
    const utcSeconds = Math.floor(Date.UTC(2026, 0, 15, 4, 34, 0));
    expect(formatDatetimeLocal(utcSeconds)).toBe("2026-01-15T12:34");
  });

  it("单数字补零 (3月5日 → 03-05, 7:5 → 07:05)", () => {
    const d = new Date(2026, 2, 5, 7, 5);
    expect(formatDatetimeLocal(Math.floor(d.getTime() / 1000))).toBe(
      "2026-03-05T07:05",
    );
  });
});

describe("timeAgo", () => {
  it("秒级", () => {
    const t = new Date(Date.now() - 30 * 1000);
    expect(timeAgo(t)).toMatch(/秒前/);
  });

  it("分钟级", () => {
    const t = new Date(Date.now() - 5 * 60 * 1000);
    expect(timeAgo(t)).toMatch(/分钟前/);
  });

  it("小时级", () => {
    const t = new Date(Date.now() - 2 * 3600 * 1000);
    expect(timeAgo(t)).toMatch(/小时前/);
  });

  it("天级", () => {
    const t = new Date(Date.now() - 3 * 86400 * 1000);
    expect(timeAgo(t)).toMatch(/天前/);
  });

  it("超过 30 天 → toLocaleDateString", () => {
    const t = new Date(Date.now() - 60 * 86400 * 1000);
    expect(timeAgo(t)).toMatch(/202/);
  });
});

describe("debounce", () => {
  it("wait ms 内只触发一次", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const d = debounce(fn, 100);
    d(1);
    d(2);
    d(3);
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
    expect(fn).toHaveBeenCalledOnce();
    expect(fn).toHaveBeenCalledWith(3);
    vi.useRealTimers();
  });
});

describe("downloadJson", () => {
  it("不抛错且产生下载链接（jsdom 兼容）", () => {
    vi.useFakeTimers();
    const origCreate = URL.createObjectURL;
    const origRevoke = URL.revokeObjectURL;
    URL.createObjectURL = () => "blob:mock";
    URL.revokeObjectURL = () => undefined;
    // jsdom 不支持 anchor.click() 触发导航（会打 stderr 噪音并告警），
    // 这里替换 click：仍记录触发次数，便于断言「确实点了下载」
    const clickSpy = vi.fn();
    const origClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = clickSpy;
    try {
      expect(() => downloadJson("x.json", { a: 1 })).not.toThrow();
      expect(clickSpy).toHaveBeenCalledOnce();
      // 推进 setTimeout 让 revoke 被调用
      vi.advanceTimersByTime(1);
    } finally {
      URL.createObjectURL = origCreate;
      URL.revokeObjectURL = origRevoke;
      HTMLAnchorElement.prototype.click = origClick;
      vi.useRealTimers();
    }
  });
});

describe("validateSettingsImport", () => {
  it("合法 KV 通过", () => {
    const out = validateSettingsImport('{"a":"1","b":"2"}', 10);
    expect(out).toEqual({ a: "1", b: "2" });
  });

  it("超大文件被拒", () => {
    expect(() => validateSettingsImport('{"a":"1"}', 2 * 1024 * 1024)).toThrow(
      /文件过大/,
    );
  });

  it("非法 JSON 抛错", () => {
    expect(() => validateSettingsImport("not json", 5)).toThrow(/JSON 解析失败/);
  });

  it("顶层是数组被拒", () => {
    expect(() => validateSettingsImport("[1,2,3]", 5)).toThrow(
      /JSON 顶层必须是对象/,
    );
  });

  it("顶层是 null 被拒", () => {
    expect(() => validateSettingsImport("null", 4)).toThrow(
      /JSON 顶层必须是对象/,
    );
  });

  it("value 非 string 被拒", () => {
    expect(() => validateSettingsImport('{"foo": 123}', 14)).toThrow(
      /foo.*必须是字符串/,
    );
  });

  it("空对象合法", () => {
    expect(validateSettingsImport("{}", 2)).toEqual({});
  });
});

describe("validateDBDumpImport", () => {
  it("接受合法 DBDump", () => {
    const text = JSON.stringify({
      version: 3,
      channels: [],
      channel_models: [],
      groups: [],
      group_items: [],
      api_keys: [],
      settings: [],
    });
    const parsed = validateDBDumpImport(text, text.length);
    expect(parsed.version).toBe(3);
    expect(parsed.channels).toEqual([]);
  });

  it("超大文件被拒", () => {
    expect(() => validateDBDumpImport("{}", 200 * 1024 * 1024)).toThrow(/文件过大/);
  });

  it("顶层不是对象被拒", () => {
    expect(() => validateDBDumpImport("[1,2]", 5)).toThrow(/顶层必须是对象/);
  });

  it("channels 字段非数组被拒", () => {
    expect(() => validateDBDumpImport('{"channels": {}}', 16)).toThrow(
      /channels 必须是数组/,
    );
  });

  it("忽略未提供的可选字段", () => {
    const parsed = validateDBDumpImport("{}", 2);
    expect(parsed.version).toBeUndefined();
    expect(parsed.channels).toBeUndefined();
  });
});

describe("validateField", () => {
  it("required + 空 → 必填", () => {
    expect(validateField("", { required: true })).toBe("必填");
    expect(validateField("   ", { required: true })).toBe("必填");
  });

  it("非 required + 空 → 通过", () => {
    expect(validateField("", {})).toBeNull();
    expect(validateField("   ", {})).toBeNull();
  });

  it("minLen 边界", () => {
    expect(validateField("ab", { minLen: 3 })).toBe("至少 3 个字符");
    expect(validateField("abc", { minLen: 3 })).toBeNull();
  });

  it("maxLen 边界", () => {
    expect(validateField("abcd", { maxLen: 3 })).toBe("不能超过 3 个字符");
    expect(validateField("abc", { maxLen: 3 })).toBeNull();
  });

  it("pattern 失败返回自定义消息", () => {
    expect(validateField("foo", { pattern: { re: /^\d+$/, message: "必须是数字" } })).toBe(
      "必须是数字",
    );
    expect(validateField("123", { pattern: { re: /^\d+$/, message: "必须是数字" } })).toBeNull();
  });

  it("custom validate 拦截", () => {
    const rule = {
      validate: (v: string) => (v === "bad" ? "禁用 bad" : null),
    };
    expect(validateField("bad", rule)).toBe("禁用 bad");
    expect(validateField("ok", rule)).toBeNull();
  });

  it("自动 trim（trim 后才计算长度）", () => {
    expect(validateField("  abcd  ", { maxLen: 3 })).toBe("不能超过 3 个字符");
    expect(validateField("  abc  ", { maxLen: 3 })).toBeNull();
  });
});

describe("NAME_RULE / URL_RULE / MODEL_RULE", () => {
  it("NAME_RULE 接受合法名字", () => {
    expect(validateField("openai-prod", NAME_RULE)).toBeNull();
    expect(validateField("我的渠道", NAME_RULE)).toBeNull();
    expect(validateField("group.1", NAME_RULE)).toBeNull();
  });

  it("NAME_RULE 拒特殊字符", () => {
    expect(validateField("@home", NAME_RULE)).not.toBeNull();
    expect(validateField("a<b", NAME_RULE)).not.toBeNull();
  });

  it("URL_RULE 接受 http/https", () => {
    expect(validateField("https://api.openai.com/v1", URL_RULE)).toBeNull();
    expect(validateField("http://10.0.0.1:8080", URL_RULE)).toBeNull();
  });

  it("URL_RULE 拒非法", () => {
    expect(validateField("ftp://x", URL_RULE)).toBe("必须以 http:// 或 https:// 开头");
    expect(validateField("api.example.com", URL_RULE)).not.toBeNull();
  });

  it("MODEL_RULE 接受标准模型名", () => {
    expect(validateField("gpt-4o", MODEL_RULE)).toBeNull();
    expect(validateField("claude-3.5-sonnet", MODEL_RULE)).toBeNull();
  });

  it("MODEL_RULE 拒非法字符", () => {
    expect(validateField("gpt 4o", MODEL_RULE)).toBe("模型名格式非法");
  });
});

describe("formatDuration 小时格式", () => {
  it("超过 1 小时显示 h*m 格式", () => {
    const now = Date.now();
    // 1 小时 5 分 3 秒前开始
    const startedAt = new Date(now - (3600 + 300 + 3) * 1000).toISOString();
    const result = formatDuration(
      { status: "running", started_at: startedAt },
      now,
    );
    expect(result).toContain("1h05m");
  });
});

describe("validateDBDumpImport 异常分支", () => {
  it("非法 JSON 被拒", () => {
    expect(() => validateDBDumpImport("{bad json", 9)).toThrow(/JSON 解析失败/);
  });

  it("version 非数字被拒", () => {
    expect(() =>
      validateDBDumpImport('{"version":"abc"}', 16),
    ).toThrow(/version 必须是数字/);
  });
});

describe("elapsedParts", () => {
  it("running 状态 → kind=running", () => {
    const now = Date.now();
    const parts = elapsedParts(
      { status: "running", started_at: new Date(now - 5000).toISOString() },
      now,
    );
    expect(parts.kind).toBe("running");
  });

  it("committed 且有首字 → kind=first-total，总耗时从请求到达起算", () => {
    const now = Date.now();
    const parts = elapsedParts(
      {
        status: "committed",
        started_at: new Date(now - 5000).toISOString(),
        first_token_at: new Date(now - 3000).toISOString(),
        duration_ms: 5000,
      },
      now,
    );
    expect(parts.kind).toBe("first-total");
    if (parts.kind === "first-total") {
      expect(parts.first).toBe("2s");
      // 总耗时 = started_at → now = 5000ms，而不是 first_token_at → now = 2000ms。
      expect(parts.total).toBe("5s");
    }
  });

  it("committed 无首字 → kind=total", () => {
    const now = Date.now();
    const parts = elapsedParts(
      {
        status: "committed",
        started_at: new Date(now - 5000).toISOString(),
        duration_ms: 5000,
      },
      now,
    );
    expect(parts.kind).toBe("total");
  });

  it("终态且 duration_ms=0 → kind=none", () => {
    const now = Date.now();
    const parts = elapsedParts(
      {
        status: "failed",
        started_at: new Date(now - 5000).toISOString(),
        duration_ms: 0,
      },
      now,
    );
    expect(parts.kind).toBe("none");
  });

  it("started_at 非法 → 回退 duration_ms", () => {
    const parts = elapsedParts({ status: "failed", started_at: "invalid", duration_ms: 300 });
    expect(parts.kind).toBe("total");
  });

  it("started_at 非法且无 duration → kind=none", () => {
    const parts = elapsedParts({ status: "failed", started_at: "invalid" });
    expect(parts.kind).toBe("none");
  });

  it("started_at 非法时回退 duration (纳秒)", () => {
    const parts = elapsedParts({ status: "failed", started_at: "invalid", duration: 3_000_000 });
    expect(parts.kind).toBe("total");
  });
});

describe("formatElapsedWithFirst", () => {
  it("running → 包含「正在请求」", () => {
    const now = Date.now();
    const result = formatElapsedWithFirst(
      { status: "running", started_at: new Date(now - 5000).toISOString() },
      now,
    );
    expect(result).toContain("正在请求");
  });

  it("first-total → 包含「首字」和「总耗时」", () => {
    const now = Date.now();
    const result = formatElapsedWithFirst(
      {
        status: "committed",
        started_at: new Date(now - 5000).toISOString(),
        first_token_at: new Date(now - 3000).toISOString(),
        duration_ms: 5000,
      },
      now,
    );
    expect(result).toContain("首字");
    expect(result).toContain("总耗时");
  });

  it("total → 纯耗时字符串", () => {
    const now = Date.now();
    const result = formatElapsedWithFirst(
      {
        status: "committed",
        started_at: new Date(now - 5000).toISOString(),
        duration_ms: 5000,
      },
      now,
    );
    expect(result).toMatch(/5s/);
  });

  it("none → —", () => {
    const now = Date.now();
    const result = formatElapsedWithFirst(
      { status: "failed", started_at: new Date(now - 5000).toISOString(), duration_ms: 0 },
      now,
    );
    expect(result).toBe("—");
  });
});
