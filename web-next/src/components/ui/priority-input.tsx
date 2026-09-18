import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Minus, Plus } from "lucide-react";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { Channel } from "@/lib/types";
import { cn } from "@/lib/utils";

/**
 * PriorityInput 渠道优先级行内编辑：
 *  - 加减按钮走防抖保存（连续点击仅保留最终值），失焦/回车立即提交；
 *  - 保存请求经串行队列，避免同渠道并发补丁乱序覆盖；
 *  - 乐观更新写 ["channels"] 缓存，失败回滚快照并提示，之后 invalidate 对齐服务端；
 *  - 服务端值追平最近一次提交的期望值之前，列表刷新不覆盖用户草稿。
 */
const SORT_DEBOUNCE_MS = 500;

export function PriorityInput({
  channel,
  inputClassName,
}: {
  channel: Channel;
  /** 输入框尺寸样式：渠道页与自定义模型页的表格/卡片布局各自传入。 */
  inputClassName: string;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState(() => String(channel.sort ?? 0));
  // dirtyRef 标记正在编辑或等待保存回填，阻止列表刷新覆盖草稿；
  // lastSavedRef 记录最近一次提交的期望值，服务端值追平后解除 dirty 并同步显示。
  const dirtyRef = useRef(false);
  const lastSavedRef = useRef<number | null>(null);
  const timerRef = useRef<number | null>(null);
  // queueRef 串行化本渠道的保存请求，避免并发补丁互相覆盖。
  const queueRef = useRef<Promise<void>>(Promise.resolve());

  useEffect(() => {
    if (lastSavedRef.current !== null && channel.sort === lastSavedRef.current) {
      lastSavedRef.current = null;
      dirtyRef.current = false;
      setDraft(String(channel.sort ?? 0));
    } else if (!dirtyRef.current) {
      setDraft(String(channel.sort ?? 0));
    }
  }, [channel.sort]);

  // 卸载时清理未触发的防抖定时器。
  useEffect(
    () => () => {
      if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    },
    [],
  );

  // saveNow 以乐观更新写入 sort：先本地打补丁，失败回滚快照并提示。
  function saveNow(next: number) {
    queueRef.current = queueRef.current
      .then(async () => {
        await qc.cancelQueries({ queryKey: ["channels"] });
        const previous = qc.getQueryData<Channel[]>(["channels"]);
        if (previous) {
          qc.setQueryData<Channel[]>(
            ["channels"],
            previous.map((c) =>
              c.id === channel.id ? { ...c, sort: next } : c,
            ),
          );
        }
        try {
          await api.updateChannel({ id: channel.id, sort: next });
        } catch (e) {
          if (previous) qc.setQueryData(["channels"], previous);
          toast.error(e instanceof Error ? e.message : "保存优先级失败");
        } finally {
          qc.invalidateQueries({ queryKey: ["channels"] });
        }
      })
      .catch(() => {
        // 吞掉链路异常，保证后续保存任务继续执行。
      });
  }

  // scheduleSave 更新草稿并安排防抖保存，连续点击仅保留最终值。
  function scheduleSave(next: number) {
    setDraft(String(next));
    dirtyRef.current = true;
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null;
      lastSavedRef.current = next;
      saveNow(next);
    }, SORT_DEBOUNCE_MS);
  }

  // commitSort 失焦/回车提交：非法输入回退服务端值，值未变取消防抖，否则立即保存。
  function commitSort(raw: string) {
    const trimmed = raw.trim();
    if (trimmed === "" || !/^-?\d+$/.test(trimmed)) {
      dirtyRef.current = false;
      lastSavedRef.current = null;
      setDraft(String(channel.sort ?? 0));
      return;
    }
    const next = parseInt(trimmed, 10);
    setDraft(String(next));
    if (next === (channel.sort ?? 0)) {
      dirtyRef.current = false;
    } else {
      dirtyRef.current = true;
      lastSavedRef.current = next;
    }
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    if (next !== (channel.sort ?? 0)) saveNow(next);
  }

  return (
    // 卡片整体可点击打开编辑器，行内控件必须阻断冒泡。
    <div
      className="inline-flex items-center gap-0.5"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        aria-label={`降低优先级 ${channel.name}`}
        onClick={() => scheduleSave((parseInt(draft, 10) || 0) - 1)}
        className="flex h-6 w-5 shrink-0 items-center justify-center rounded text-ink-muted transition-colors hover:bg-surface-subtle hover:text-ink disabled:opacity-40"
      >
        <Minus className="h-3 w-3" aria-hidden />
      </button>
      <input
        type="number"
        step="1"
        className={cn("no-spin", inputClassName)}
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value);
          dirtyRef.current = true;
        }}
        onBlur={(e) => commitSort(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            (e.target as HTMLInputElement).blur();
          }
        }}
        title="优先级：越大越靠前，允许重复和负数，相同值按名称排序"
        aria-label={`优先级 ${channel.name}`}
      />
      <button
        type="button"
        aria-label={`提高优先级 ${channel.name}`}
        onClick={() => scheduleSave((parseInt(draft, 10) || 0) + 1)}
        className="flex h-6 w-5 shrink-0 items-center justify-center rounded text-ink-muted transition-colors hover:bg-surface-subtle hover:text-ink disabled:opacity-40"
      >
        <Plus className="h-3 w-3" aria-hidden />
      </button>
    </div>
  );
}
