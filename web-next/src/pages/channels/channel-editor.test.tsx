import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Channel, ChannelTestResult } from "@/lib/types";
import { DEFAULT_TEST_MESSAGE } from "@/lib/constants";
import { zhCN } from "@/locales/zh-CN";
import { sampleChannel } from "@/test/fixtures/channels";
import { ChannelEditor } from "./channel-editor";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function reply(content: string): ChannelTestResult {
  return { model: "gpt-4o", content, latency_ms: 123, prompt_tokens: 2, completion_tokens: 1 };
}

function mountEditor(initial: Channel | null = null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(["setting", "channel_test_message"], { key: "channel_test_message", value: DEFAULT_TEST_MESSAGE });
  const onClose = vi.fn();
  const onSaved = vi.fn();
  const view = (channel: Channel | null) => (
    <StrictMode>
      <QueryClientProvider client={client}>
        <ChannelEditor channel={channel} onClose={onClose} onSaved={onSaved} />
      </QueryClientProvider>
    </StrictMode>
  );
  const rendered = render(view(initial));
  return {
    ...rendered,
    client,
    show: (channel: Channel | null) => rendered.rerender(view(channel)),
  };
}

async function openModels(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "模型 (2)" }));
}

beforeEach(() => {
  vi.spyOn(api, "getSetting").mockResolvedValue({ key: "channel_test_message", value: DEFAULT_TEST_MESSAGE });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("渠道编辑器测试结果", () => {
  it.each(["success", "failure"])("从关闭态打开及重开后显示 %s", async (outcome) => {
    const test = vi.spyOn(api, "testChannel");
    if (outcome === "success") test.mockResolvedValue(reply("当前模型回复"));
    else test.mockRejectedValue(new Error("上游拒绝了此密钥"));
    const user = userEvent.setup();
    const editor = mountEditor();
    for (let round = 0; round < 2; round += 1) {
      editor.show(sampleChannel);
      await openModels(user);
      await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
      expect(await screen.findByText(outcome === "success" ? "当前模型回复" : "上游拒绝了此密钥")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "测试 gpt-4o" })).toBeEnabled();
      editor.show(null);
    }
    expect(test).toHaveBeenCalledTimes(2);
    expect(test).toHaveBeenLastCalledWith(sampleChannel.id, "gpt-4o", DEFAULT_TEST_MESSAGE, undefined);
  });

  it.each(["reopen", "switch"])("%s 后旧请求不能覆盖结果或清掉新请求的加载状态", async (action) => {
    const old = deferred<ChannelTestResult>();
    const current = deferred<ChannelTestResult>();
    vi.spyOn(api, "testChannel").mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const user = userEvent.setup();
    const editor = mountEditor(sampleChannel);
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
    if (action === "reopen") editor.show(null);
    editor.show(action === "switch" ? { ...sampleChannel, id: 2, name: "second-channel" } : sampleChannel);
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
    await act(async () => { old.resolve(reply("过期结果")); });
    expect(screen.queryByText("过期结果")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "测试 gpt-4o" })).toBeDisabled();
    await act(async () => { current.resolve(reply("本轮结果")); });
    expect(await screen.findByText("本轮结果")).toBeInTheDocument();
  });

  it("切换密钥后丢弃旧密钥的失败结果", async () => {
    const old = deferred<ChannelTestResult>();
    const current = deferred<ChannelTestResult>();
    const test = vi.spyOn(api, "testChannel").mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const user = userEvent.setup();
    mountEditor({ ...sampleChannel, keys: [...sampleChannel.keys, { id: "k2", key: "", key_masked: "...EFGH", remark: "备用" }] });
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "按模型测试使用的密钥" }), "k2");
    await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
    await act(async () => { old.reject(new Error("旧密钥的错误")); });
    expect(screen.queryByText("旧密钥的错误")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "测试 gpt-4o" })).toBeDisabled();
    expect(test).toHaveBeenLastCalledWith(sampleChannel.id, "gpt-4o", DEFAULT_TEST_MESSAGE, "k2");
    await act(async () => { current.resolve(reply("备用密钥结果")); });
    expect(await screen.findByText("备用密钥结果")).toBeInTheDocument();
  });

  it("旧批次结束不会继续派发或清掉新批次状态", async () => {
    const old = Array.from({ length: 4 }, () => deferred<ChannelTestResult>());
    const current = Array.from({ length: 2 }, () => deferred<ChannelTestResult>());
    const test = vi.spyOn(api, "testChannel");
    for (const task of [...old, ...current]) test.mockReturnValueOnce(task.promise);
    const user = userEvent.setup();
    const channel = {
      ...sampleChannel,
      models: Array.from({ length: 5 }, (_, index) => ({ ...sampleChannel.models[0], id: 200 + index, name: `m${index}` })),
    };
    const editor = mountEditor(channel);
    await user.click(screen.getByRole("button", { name: "模型 (5)" }));
    await user.click(screen.getByRole("button", { name: "全部测试" }));
    expect(test).toHaveBeenCalledTimes(4);
    editor.show(null);
    editor.show(sampleChannel);
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "全部测试" }));
    await act(async () => { for (const task of old) task.resolve(reply("旧批次")); });
    expect(test).toHaveBeenCalledTimes(6);
    expect(screen.getByRole("button", { name: "全部测试" })).toBeDisabled();
    expect(screen.queryByText("旧批次")).not.toBeInTheDocument();
    await act(async () => { for (const task of current) task.resolve(reply("新批次")); });
    await waitFor(() => expect(screen.getByRole("button", { name: "全部测试" })).toBeEnabled());
  });

  it("成功但没有文本时显示说明", async () => {
    vi.spyOn(api, "testChannel").mockResolvedValue(reply(""));
    const user = userEvent.setup();
    mountEditor(sampleChannel);
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "测试 gpt-4o" }));
    expect(await screen.findByText(zhCN.channelTest.emptyContent)).toBeInTheDocument();
  });

  it("逐密钥测试使用自定义消息且重开后允许立即开始新测试", async () => {
    const old = deferred<Awaited<ReturnType<typeof api.testChannelKeys>>>();
    const test = vi.spyOn(api, "testChannelKeys").mockReturnValueOnce(old.promise).mockResolvedValueOnce([
      { key_id: "k1", label: "#1", ok: true, content: "新密钥结果", elapsed_ms: 10 },
    ]);
    const user = userEvent.setup();
    const editor = mountEditor(sampleChannel);
    editor.client.setQueryData(["setting", "channel_test_message"], { key: "channel_test_message", value: "自定义问题" });
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "按密钥测试 (1)" }));
    editor.show(null);
    editor.show(sampleChannel);
    await openModels(user);
    await user.click(screen.getByRole("button", { name: "按密钥测试 (1)" }));
    expect(await screen.findByText("新密钥结果")).toBeInTheDocument();
    await act(async () => {
      old.resolve([{ key_id: "k1", label: "#1", ok: false, error: "旧密钥批次错误", elapsed_ms: 20 }]);
    });
    expect(screen.queryByText("旧密钥批次错误")).not.toBeInTheDocument();
    expect(test).toHaveBeenLastCalledWith(sampleChannel.id, "gpt-4o", "自定义问题");
  });
});
