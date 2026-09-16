import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Search } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  loadChannelsPage,
  loadChatPage,
  loadCustomModelsPage,
  loadDashboardPage,
  loadGroupsPage,
  loadKeysPage,
  loadLogsPage,
  loadMaskPage,
  loadModelEvalPage,
  loadSettingsPage,
} from "@/lib/route-loaders";

/**
 * ⌘K 命令面板 —— DESIGN.md §3
 *  - 导航：六个页面
 *  - 动作：新建渠道 / 新建密钥 / 切换主题
 *  - 键盘：↑↓ 选择 / Enter 执行 / Esc 关闭
 */
export function CommandPalette({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const items = useMemo<
    { label: string; hint: string; run: () => void; load?: () => Promise<unknown> }[]
  >(
    () => [
      { label: "前往 总览", hint: "导航", run: () => navigate("/dashboard"), load: loadDashboardPage },
      { label: "前往 渠道", hint: "导航", run: () => navigate("/channels"), load: loadChannelsPage },
      { label: "前往 自定义模型", hint: "导航", run: () => navigate("/custom-models"), load: loadCustomModelsPage },
      { label: "前往 分组", hint: "导航", run: () => navigate("/groups"), load: loadGroupsPage },
      { label: "前往 模型评估", hint: "导航", run: () => navigate("/model-eval"), load: loadModelEvalPage },
      { label: "前往 脱敏", hint: "导航", run: () => navigate("/mask"), load: loadMaskPage },
      { label: "前往 对话", hint: "导航", run: () => navigate("/chat"), load: loadChatPage },
      { label: "前往 API 密钥", hint: "导航", run: () => navigate("/keys"), load: loadKeysPage },
      { label: "前往 日志", hint: "导航", run: () => navigate("/logs"), load: loadLogsPage },
      { label: "前往 设置", hint: "导航", run: () => navigate("/settings"), load: loadSettingsPage },
    ],
    [navigate],
  );

  const filtered = useMemo(
    () =>
      items.filter((i) => i.label.toLowerCase().includes(q.toLowerCase())),
    [items, q],
  );

  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  // 键盘选中项变化时预加载对应 chunk（§3.3）：覆盖 ↑↓ 导航的键盘用户，
  // 与列表项 onMouseEnter 的鼠标预加载互补。loader 幂等，重复调用只请求一次。
  useEffect(() => {
    if (!open) return;
    const item = filtered[sel];
    if (item?.load) void item.load();
  }, [open, filtered, sel]);

  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        setSel((s) => Math.min(s + 1, filtered.length - 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setSel((s) => Math.max(s - 1, 0));
      } else if (e.key === "Enter") {
        e.preventDefault();
        const item = filtered[sel];
        if (item) {
          onClose();
          item.run();
        }
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, filtered, sel, onClose]);

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="命令面板"
      onClick={onClose}
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/30 px-4 pt-[15vh] backdrop-blur-sm"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="glass-overlay w-full max-w-lg overflow-hidden rounded-card"
      >
        <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
          <Search className="h-4 w-4 text-ink-muted" />
          <input
            ref={inputRef}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setSel(0);
            }}
            placeholder="输入命令或页面名…"
            className="flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none"
          />
          <kbd className="rounded border border-border bg-surface-subtle px-1.5 py-0.5 text-[10px] text-ink-muted">
            Esc
          </kbd>
        </div>
        <div className="max-h-80 overflow-y-auto p-1.5">
          {filtered.length === 0 && (
            <div className="px-3 py-6 text-center text-sm text-ink-muted">
              无匹配结果
            </div>
          )}
          {filtered.map((it, i) => (
            <button
              key={it.label}
              onMouseEnter={() => {
                setSel(i);
                // 鼠标悬停列表项时预加载对应 chunk（§3.3），与键盘选中项的预加载互补
                if (it.load) void it.load();
              }}
              onClick={() => {
                onClose();
                it.run();
              }}
              className={cn(
                "flex w-full items-center justify-between gap-3 rounded-md px-2.5 py-2 text-left text-sm transition-colors",
                sel === i
                  ? "bg-primary/[0.12] text-primary-text"
                  : "text-ink-muted hover:bg-surface-subtle",
              )}
            >
              <span>{it.label}</span>
              <span className="text-[10px] uppercase tracking-wider text-ink-muted">
                {it.hint}
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
