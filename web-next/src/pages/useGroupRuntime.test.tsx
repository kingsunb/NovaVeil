import { describe, expect, it, vi, afterEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import {
  formatCountdown,
  remainingSeconds,
  useGroupRuntime,
  useNow,
} from "./useGroupRuntime";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  withCredentials = false;
  onopen: ((e: Event) => void) | null = null;
  listeners: Record<string, ((e: Event) => void)[]> = {};

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = !!init?.withCredentials;
    FakeEventSource.instances.push(this);
  }
  addEventListener(name: string, fn: (e: Event) => void) {
    (this.listeners[name] ||= []).push(fn);
  }
  close() {}
  fireOpen() {
    this.onopen?.(new Event("open"));
  }
  fireEvent(name: string, data: unknown) {
    const event = new MessageEvent(name, { data: JSON.stringify(data) });
    this.listeners[name]?.forEach((fn) => fn(event));
  }
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  FakeEventSource.instances = [];
});

describe("remainingSeconds / formatCountdown", () => {
  it("截止未来返回 ceil 秒数；到期与无截止返回 0", () => {
    const now = 1_000_000;
    expect(remainingSeconds(now + 30_000, now)).toBe(30);
    expect(remainingSeconds(now + 500, now)).toBe(1);
    expect(remainingSeconds(now - 1, now)).toBe(0);
    expect(remainingSeconds(0, now)).toBe(0);
  });

  it("formatCountdown：<60s 显示秒，≥60s 显示 mm:ss", () => {
    expect(formatCountdown(45)).toBe("45s");
    expect(formatCountdown(0)).toBe("0s");
    expect(formatCountdown(1845)).toBe("30:45");
    expect(formatCountdown(60)).toBe("1:00");
  });
});

describe("useNow", () => {
  it("订阅时取当前时间，interval 走动后更新", async () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useNow());
    const first = result.current;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(result.current).toBeGreaterThanOrEqual(first + 1000);
    vi.useRealTimers();
  });
});

describe("useGroupRuntime", () => {
  it("订阅 event:runtime 流并按 group_id upsert", async () => {
    vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
    const { result } = renderHook(() => useGroupRuntime());
    const es = FakeEventSource.instances[0]!;
    expect(es.url).toBe("/api/v1/group/runtime/stream");
    expect(es.withCredentials).toBe(true);

    act(() => {
      es.fireEvent("runtime", {
        group_id: 7,
        current_item_id: 2,
        probe_item_id: 0,
        affinity_until: 123,
        cooldowns: { "3": 456 },
        levels: {},
        half_opens: {},
        post_commit_strikes: {},
        emergency_item_id: 0,
        emergency_active: 0,
      });
    });
    await waitFor(() => {
      expect(result.current.get(7)?.current_item_id).toBe(2);
      expect(result.current.get(7)?.cooldowns["3"]).toBe(456);
    });

    // 同 group 再推一条 → 覆盖；另一 group → 新增
    act(() => {
      es.fireEvent("runtime", {
        group_id: 7,
        current_item_id: 0,
        probe_item_id: 1,
        affinity_until: 0,
        cooldowns: {},
        levels: {},
        half_opens: {},
        post_commit_strikes: {},
        emergency_item_id: 0,
        emergency_active: 0,
      });
      es.fireEvent("runtime", {
        group_id: 8,
        current_item_id: 0,
        probe_item_id: 0,
        affinity_until: 0,
        cooldowns: {},
        levels: {},
        half_opens: {},
        post_commit_strikes: {},
        emergency_item_id: 5,
        emergency_active: 2,
      });
    });
    await waitFor(() => {
      expect(result.current.get(7)?.current_item_id).toBe(0);
      expect(result.current.get(8)?.emergency_active).toBe(2);
    });
  });

  it("非法事件（缺 group_id）被忽略不崩溃", () => {
    vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
    const { result } = renderHook(() => useGroupRuntime());
    const es = FakeEventSource.instances[0]!;
    act(() => {
      es.fireEvent("runtime", { foo: "bar" });
    });
    expect(result.current.size).toBe(0);
  });
});
