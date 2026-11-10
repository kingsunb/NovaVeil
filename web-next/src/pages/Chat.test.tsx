import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import ChatPage from "./Chat";

const CHAT_SESSION_KEY = "novaveil:chat:current";

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

function jsonOk(data: unknown) {
  return new Response(JSON.stringify({ code: 200, message: "success", data }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function setScrollMetrics(el: HTMLElement, scrollHeight = 1000, clientHeight = 400) {
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: scrollHeight });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: clientHeight });
}

function seedSession() {
  localStorage.setItem(
    CHAT_SESSION_KEY,
    JSON.stringify({
      groupName: "g1",
      messages: [
        { role: "user", content: "旧消息" },
        { role: "assistant", content: "旧回复" },
      ],
    }),
  );
}

function setupGroupsFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/group/list")) return Promise.resolve(jsonOk([{ id: 1, name: "g1" }]));
      return Promise.resolve(jsonOk(null));
    }),
  );
}

describe("<ChatPage /> 滚动跟随", () => {
  const scrollTo = vi.fn();

  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem(
      "nv-auth",
      JSON.stringify({ isAuthenticated: true, username: "admin", mustChangePassword: false }),
    );
    seedSession();
    scrollTo.mockReset();
    Object.defineProperty(HTMLElement.prototype, "scrollTo", {
      configurable: true,
      value: scrollTo,
    });
  });

  it("上翻后显示回到底部按钮，点击后立即滚底", async () => {
    setupGroupsFetch();
    const { container } = render(<ChatPage />, { wrapper: Wrapper });
    expect(await screen.findByText("旧回复")).toBeInTheDocument();
    await screen.findByRole("option", { name: "g1" });

    const scroller = container.querySelector("div.overflow-y-auto") as HTMLElement;
    setScrollMetrics(scroller);
    scroller.scrollTop = 200;
    fireEvent.scroll(scroller);

    const jump = await screen.findByRole("button", { name: "回到底部" });
    scrollTo.mockClear();
    fireEvent.click(jump);

    expect(scrollTo).toHaveBeenCalledWith({ top: 1000, behavior: "auto" });
  });

  it("流式增量到达时不把上翻的读者拉回底部", async () => {
    let streamController!: ReadableStreamDefaultController<Uint8Array>;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        streamController = controller;
      },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/group/list")) {
          return Promise.resolve(jsonOk([{ id: 1, name: "g1" }]));
        }
        if (url.includes("/chat/completions")) {
          return Promise.resolve(
            new Response(stream, {
              status: 200,
              headers: { "content-type": "text/event-stream" },
            }),
          );
        }
        return Promise.resolve(jsonOk(null));
      }),
    );

    const user = userEvent.setup();
    const { container } = render(<ChatPage />, { wrapper: Wrapper });
    expect(await screen.findByText("旧回复")).toBeInTheDocument();
    await screen.findByRole("option", { name: "g1" });

    const scroller = container.querySelector("div.overflow-y-auto") as HTMLElement;
    setScrollMetrics(scroller);

    await user.type(screen.getByRole("textbox", { name: "消息输入" }), "新问题");
    await user.click(screen.getByRole("button", { name: "发送" }));
    expect(await screen.findByRole("button", { name: "停止" })).toBeInTheDocument();

    scroller.scrollTop = 0;
    fireEvent.scroll(scroller);
    await screen.findByRole("button", { name: "回到底部" });
    scrollTo.mockClear();

    await act(async () => {
      streamController.enqueue(
        new TextEncoder().encode(
          'data: {"choices":[{"delta":{"content":"流式回复"}}]}\n\n',
        ),
      );
    });
    expect(await screen.findByText(/流式回复/)).toBeInTheDocument();
    expect(scrollTo).not.toHaveBeenCalled();

    await act(async () => {
      streamController.close();
    });
    expect(await screen.findByRole("button", { name: "发送" })).toBeInTheDocument();
  });
});
