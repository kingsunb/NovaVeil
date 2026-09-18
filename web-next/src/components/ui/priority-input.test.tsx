import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Channel } from "@/lib/types";

vi.mock("@/lib/api", () => ({
  api: { updateChannel: vi.fn() },
}));
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), info: vi.fn() },
}));

import { api } from "@/lib/api";
import { toast } from "sonner";
import { PriorityInput } from "./priority-input";

const updateChannelMock = vi.mocked(api.updateChannel);
const toastErrorMock = vi.mocked(toast.error);

function makeChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: 1,
    name: "openai-prod",
    type: "openai",
    enabled: true,
    is_free: false,
    builtin: false,
    base_url: "",
    key: "",
    keys: [],
    models: [],
    fixed_reply: "",
    proxy: false,
    auto_sync: false,
    opencode_compat: false,
    pass_through_body_enabled: false,
    custom_header: [],
    model_limits: {},
    tags: [],
    sort: 0,
    rate_limit_rpm: 0,
    max_concurrent: 0,
    ...overrides,
  };
}

function setup(channel: Channel, client?: QueryClient) {
  const qc = client ?? new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const utils = render(
    <QueryClientProvider client={qc}>
      <PriorityInput channel={channel} inputClassName="w-12" />
    </QueryClientProvider>,
  );
  const rerender = (ch: Channel) =>
    utils.rerender(
      <QueryClientProvider client={qc}>
        <PriorityInput channel={ch} inputClassName="w-12" />
      </QueryClientProvider>,
    );
  return { ...utils, qc, rerender };
}

/** 刷新 saveNow 串行队列里的微任务（cancelQueries / updateChannel 等异步链）。 */
function flush() {
  return act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

const INPUT_LABEL = "优先级 openai-prod";
const MINUS_LABEL = "降低优先级 openai-prod";
const PLUS_LABEL = "提高优先级 openai-prod";

beforeEach(() => {
  updateChannelMock.mockReset();
  toastErrorMock.mockReset();
  updateChannelMock.mockResolvedValue(makeChannel());
});

describe("<PriorityInput /> 初始渲染与外部同步", () => {
  it("以 channel.sort 作为草稿值", () => {
    setup(makeChannel({ sort: 5 }));
    expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(5);
  });

  it("channel.sort 缺省（nullish）时回退 0", () => {
    setup(makeChannel({ sort: undefined as unknown as number }));
    expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(0);
  });

  it("非脏态下 channel.sort 变化同步草稿", () => {
    const { rerender } = setup(makeChannel({ sort: 3 }));
    rerender(makeChannel({ sort: 7 }));
    expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(7);
  });

  it("脏态下 channel.sort 变化不覆盖草稿", () => {
    vi.useFakeTimers();
    try {
      const { rerender } = setup(makeChannel({ sort: 3 }));
      fireEvent.change(screen.getByLabelText(INPUT_LABEL), {
        target: { value: "9" },
      });
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(9);
      rerender(makeChannel({ sort: 20 }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(9);
    } finally {
      vi.useRealTimers();
    }
  });

  it("提交后服务端值追平期望值时解除脏态并同步显示", async () => {
    vi.useFakeTimers();
    try {
      const { rerender } = setup(makeChannel({ sort: 3 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      rerender(makeChannel({ sort: 4 }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(4);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("<PriorityInput /> 加减按钮", () => {
  it("点击 + 以当前草稿 +1 防抖保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(6);
      await vi.advanceTimersByTimeAsync(500);
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 6 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("点击 - 以当前草稿 -1 防抖保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      fireEvent.click(screen.getByRole("button", { name: MINUS_LABEL }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(4);
      await vi.advanceTimersByTimeAsync(500);
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 4 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("草稿为 0 时 parseInt||0 回退 0，加减仍以 0 为基准", () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 0 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("连续点击仅保留最终值（防抖），不产生多次保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 0 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      expect(screen.getByLabelText(INPUT_LABEL)).toHaveValue(3);
      expect(updateChannelMock).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(500);
      expect(updateChannelMock).toHaveBeenCalledTimes(1);
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 3 });
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("<PriorityInput /> commitSort（失焦/回车提交）", () => {
  it("合法且不同的值立即保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "12" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 12 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("合法但与当前值相同时不保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "5" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).not.toHaveBeenCalled();
      expect(input).toHaveValue(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("空输入回退服务端值且不保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).not.toHaveBeenCalled();
      expect(input).toHaveValue(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("非整数输入回退服务端值且不保存", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "1.5" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).not.toHaveBeenCalled();
      expect(input).toHaveValue(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("负数是合法输入", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 0 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "-3" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: -3 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("提交时取消未触发的防抖定时器", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 0 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "8" } });
      fireEvent.blur(input);
      await flush();
      expect(updateChannelMock).toHaveBeenCalledTimes(1);
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 8 });
      await vi.advanceTimersByTimeAsync(600);
      expect(updateChannelMock).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("回车键触发 blur 进而提交", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "9" } });
      input.focus();
      await act(async () => {
        fireEvent.keyDown(input, { key: "Enter" });
      });
      await flush();
      expect(updateChannelMock).toHaveBeenCalledWith({ id: 1, sort: 9 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("非回车键不触发提交", async () => {
    vi.useFakeTimers();
    try {
      setup(makeChannel({ sort: 5 }));
      const input = screen.getByLabelText(INPUT_LABEL);
      fireEvent.change(input, { target: { value: "9" } });
      fireEvent.keyDown(input, { key: "ArrowDown" });
      await flush();
      expect(updateChannelMock).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("<PriorityInput /> 乐观更新与回滚", () => {
  it("保存成功时乐观写入 channels 缓存", async () => {
    vi.useFakeTimers();
    try {
      const channels = [makeChannel({ id: 1, sort: 5 }), makeChannel({ id: 2, sort: 3 })];
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      qc.setQueryData(["channels"], channels);
      setup(makeChannel({ id: 1, sort: 5 }), qc);
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      const updated = qc.getQueryData<Channel[]>(["channels"])!;
      expect(updated.find((c) => c.id === 1)!.sort).toBe(6);
      expect(updated.find((c) => c.id === 2)!.sort).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("保存失败时回滚缓存并 toast.error（Error 实例）", async () => {
    vi.useFakeTimers();
    try {
      const channels = [makeChannel({ id: 1, sort: 5 })];
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      qc.setQueryData(["channels"], channels);
      updateChannelMock.mockRejectedValue(new Error("上游 503"));
      setup(makeChannel({ id: 1, sort: 5 }), qc);
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      await flush();
      expect(toastErrorMock).toHaveBeenCalledWith("上游 503");
      const rolled = qc.getQueryData<Channel[]>(["channels"])!;
      expect(rolled.find((c) => c.id === 1)!.sort).toBe(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("保存失败且非 Error 实例时 toast 显示通用文案", async () => {
    vi.useFakeTimers();
    try {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      qc.setQueryData(["channels"], [makeChannel({ id: 1, sort: 5 })]);
      updateChannelMock.mockRejectedValue("string error");
      setup(makeChannel({ id: 1, sort: 5 }), qc);
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      await flush();
      expect(toastErrorMock).toHaveBeenCalledWith("保存优先级失败");
    } finally {
      vi.useRealTimers();
    }
  });

  it("无缓存时保存失败不回滚（previous 为空），仅提示", async () => {
    vi.useFakeTimers();
    try {
      updateChannelMock.mockRejectedValue(new Error("boom"));
      setup(makeChannel({ id: 1, sort: 5 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      await flush();
      expect(toastErrorMock).toHaveBeenCalledWith("boom");
    } finally {
      vi.useRealTimers();
    }
  });

  it("串行队列：cancelQueries 抛错被吞，后续保存仍可执行", async () => {
    vi.useFakeTimers();
    try {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      let callCount = 0;
      const cancelSpy = vi.spyOn(qc, "cancelQueries").mockImplementation(() => {
        callCount++;
        if (callCount === 1) throw new Error("cancel boom");
        return Promise.resolve();
      });
      setup(makeChannel({ id: 1, sort: 0 }), qc);
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      await flush();
      expect(cancelSpy).toHaveBeenCalledTimes(1);
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      await vi.advanceTimersByTimeAsync(500);
      await flush();
      expect(updateChannelMock).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("<PriorityInput /> 卸载清理", () => {
  it("卸载时清理未触发的防抖定时器", async () => {
    vi.useFakeTimers();
    try {
      const { unmount } = setup(makeChannel({ sort: 0 }));
      fireEvent.click(screen.getByRole("button", { name: PLUS_LABEL }));
      unmount();
      await vi.advanceTimersByTimeAsync(600);
      expect(updateChannelMock).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("无待触发定时器时卸载不报错", () => {
    const { unmount } = setup(makeChannel({ sort: 0 }));
    expect(() => unmount()).not.toThrow();
  });
});
