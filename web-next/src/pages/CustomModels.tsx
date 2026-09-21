/**
 * 自定义模型页 —— 管理固定回复渠道(type=custom)。
 * 每个条目 = 一个自定义模型：客户端以分组/模型名命中后, 中转不做任何上游请求,
 * 直接以「固定回复」文案合成响应(支持 OpenAI Chat/Responses/Anthropic 客户端协议,
 * 流式与非流式)。条目本质是 type=custom 的渠道, 因此可照常加入分组参与选路。
 */
import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, MessageSquare, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { Channel } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input, Textarea } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { Switch } from "@/components/ui/switch";
import { TableSkeleton } from "@/components/ui/skeleton";
import { QueryErrorBanner } from "@/components/ui/query-error";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn, NAME_RULE, validateField } from "@/lib/utils";
import { Select } from "@/components/ui/select";
import { EmptyState } from "@/components/ui/empty-state";
import { SearchField } from "@/components/ui/search-field";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { PriorityInput } from "@/components/ui/priority-input";
import { ViewToggle } from "@/components/ui/view-toggle";
import { useViewMode } from "@/lib/use-view-mode";

type Editing = Channel | "new" | null;
type Sort = "custom" | "name" | "status";

export default function CustomModelsPage() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Editing>(null);
  const [pendingDelete, setPendingDelete] = useState<Channel | null>(null);
  const [testingId, setTestingId] = useState<number | null>(null);
  const [search, setSearch] = useState("");
  // 默认按优先级降序（同值按名称兜底）；
  // 优先级允许重复、零值与负值，相同数值按渠道名称字母序排列。
  const [sort, setSort] = useState<Sort>("custom");
  const [viewMode, setViewMode] = useViewMode("nv-custom-view", "list");

  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ["channels"],
    queryFn: api.listChannels,
    // 兜底轮询：保存后的 refetch 延迟/丢失时最迟 30s 自愈。
    refetchInterval: 30_000,
  });
  const rows = useMemo(() => {
    const list = (data ?? []).filter((c) => c.type === "custom");
    return list
      .filter((c) =>
        search
          ? c.name.toLowerCase().includes(search.toLowerCase()) ||
            (c.models[0]?.name ?? "").toLowerCase().includes(search.toLowerCase())
          : true,
      )
      .sort((a, b) => {
        if (sort === "custom")
          return (b.sort ?? 0) - (a.sort ?? 0) || a.name.localeCompare(b.name);
        if (sort === "name") return a.name.localeCompare(b.name);
        // status: 启用优先, 同状态按名称
        return Number(b.enabled) - Number(a.enabled) || a.name.localeCompare(b.name);
      });
  }, [data, search, sort]);

  const invalidate = () => qc.invalidateQueries({ queryKey: ["channels"] });

  // 优先级行内编辑已抽到 <PriorityInput />（components/ui/priority-input.tsx）。

  const enableMut = useMutation({
    mutationFn: (input: { id: number; enabled: boolean }) =>
      api.updateChannel({ id: input.id, enabled: input.enabled }),
    // 启停前取消在途列表 refetch，避免旧响应盖掉刚切换的状态。
    onMutate: () => qc.cancelQueries({ queryKey: ["channels"] }),
    onSuccess: invalidate,
    onError: (e: Error) => toast.error(e.message || "操作失败"),
  });

  const deleteMut = useMutation({
    mutationFn: (id: number) => api.deleteChannel(id),
    onMutate: () => qc.cancelQueries({ queryKey: ["channels"] }),
    onSuccess: () => {
      toast.success("已删除");
      setPendingDelete(null);
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message || "删除失败"),
  });

  const testMut = useMutation({
    mutationFn: (input: { id: number; model?: string }) =>
      api.testChannel(input.id, input.model),
    // 只清掉自己那行的 testing 态：连点两行时，先返回的请求不能清掉后一行的指示。
    onSettled: (_data, _error, variable) => {
      const id = (variable as { id: number }).id;
      setTestingId((cur) => (cur === id ? null : cur));
    },
    onSuccess: (r) => {
      toast.success(`连通 (${r.latency_ms}ms)`);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="space-y-4">
      <PageToolbar
        leading={
          <p className="max-w-xl text-xs leading-relaxed text-ink-muted">
            自定义模型命中后不做上游请求，直接以配置的固定文案回复；可加入分组参与选路。
          </p>
        }
        trailing={
          <>
            <ViewToggle value={viewMode} onChange={setViewMode} />
            <SearchField
              value={search}
              onChange={setSearch}
              placeholder="搜索名称/模型名"
              aria-label="搜索自定义模型"
              inputClassName="h-8 w-52 pl-8"
            />
            <Select
              className="text-xs"
              value={sort}
              onChange={(e) => setSort(e.target.value as Sort)}
              aria-label="排序"
            >
              <option value="custom">按优先级</option>
              <option value="name">按名称</option>
              <option value="status">按状态</option>
            </Select>
            <Button
              variant="primary"
              size="sm"
              className="gap-1.5"
              onClick={() => setEditing("new")}
            >
              <Plus className="h-3.5 w-3.5" aria-hidden />
              新建自定义模型
            </Button>
          </>
        }
      />

      {isLoading ? (
        <TableSkeleton rows={4} />
      ) : isError ? (
        <QueryErrorBanner onRetry={() => refetch()} />
      ) : rows.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Bot className="h-5 w-5" aria-hidden />}
            title={
              search
                ? "没有匹配的自定义模型"
                : "还没有自定义模型"
            }
            hint={search ? undefined : "点右上角创建，客户端命中即返回固定文案"}
          />
        </Card>
      ) : viewMode === "list" ? (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs text-ink-muted">
                  <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">模型名</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">固定回复</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">状态</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium" title="越大越靠前，允许重复和负数，相同值按名称排序">优先级</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((c) => {
                  const modelName = c.models[0]?.name ?? "—";
                  const testing = testingId === c.id;
                  return (
                    <tr
                      key={c.id}
                      className="border-b border-border/60 transition-colors last:border-b-0 hover:bg-surface-subtle/60"
                    >
                      <td className="px-4 py-2.5 font-medium text-ink">{c.name}</td>
                      <td className="mono px-4 py-2.5 text-ink-muted">{modelName}</td>
                      <td
                        className="max-w-[280px] truncate px-4 py-2.5 text-ink-muted"
                        title={c.fixed_reply}
                      >
                        {c.fixed_reply || <span className="text-destructive">未配置回复文案</span>}
                      </td>
                      <td className="px-4 py-2.5">
                        <div className="flex items-center gap-2">
                          <Switch
                            checked={c.enabled}
                            onCheckedChange={(v) =>
                              enableMut.mutate({ id: c.id, enabled: v })
                            }
                          />
                          {c.enabled ? (
                            <Pill tone="success">启用</Pill>
                          ) : (
                            <Pill tone="neutral">停用</Pill>
                          )}
                        </div>
                      </td>
                      <td
                        onClick={(e) => {
                          e.stopPropagation();
                          (e.currentTarget.querySelector("input") as HTMLInputElement | null)?.focus();
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                        className="cursor-text whitespace-nowrap px-4 py-2.5 text-right"
                      >
                        <PriorityInput
                          channel={c}
                          inputClassName="h-7 w-24 rounded-control border border-border bg-card px-2 text-right text-sm text-ink"
                        />
                      </td>
                      <td className="px-4 py-2.5">
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 gap-1 px-2 text-xs"
                            disabled={testing || !c.enabled || !c.models[0]}
                            loading={testing}
                            aria-label={`测试自定义模型 ${c.name}`}
                            onClick={() => {
                              setTestingId(c.id);
                              testMut.mutate({ id: c.id, model: c.models[0]?.name });
                            }}
                          >
                            测试
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            aria-label={`编辑自定义模型 ${c.name}`}
                            onClick={() => setEditing(c)}
                          >
                            <Pencil className="h-3.5 w-3.5" aria-hidden />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-destructive hover:bg-destructive/10"
                            aria-label={`删除自定义模型 ${c.name}`}
                            onClick={() => setPendingDelete(c)}
                          >
                            <Trash2 className="h-3.5 w-3.5" aria-hidden />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {rows.map((c) => {
            const modelName = c.models[0]?.name ?? "—";
            const testing = testingId === c.id;
            return (
              <Card key={c.id}>
                <CardHeader>
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-semibold text-ink">{c.name}</span>
                    <div className="flex shrink-0 items-center gap-2">
                      <Switch
                        checked={c.enabled}
                        onCheckedChange={(v) =>
                          enableMut.mutate({ id: c.id, enabled: v })
                        }
                      />
                      {c.enabled ? (
                        <Pill tone="success">启用</Pill>
                      ) : (
                        <Pill tone="neutral">停用</Pill>
                      )}
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="space-y-2">
                  <div className="flex items-center gap-2 text-xs">
                    <span className="shrink-0 text-ink-muted">模型名</span>
                    <span className="mono min-w-0 flex-1 truncate text-ink" title={modelName}>
                      {modelName}
                    </span>
                  </div>
                  <div className="flex items-center gap-2 text-xs">
                    <span className="shrink-0 text-ink-muted">固定回复</span>
                    <span
                      className="min-w-0 flex-1 truncate text-ink"
                      title={c.fixed_reply}
                    >
                      {c.fixed_reply || (
                        <span className="text-destructive">未配置</span>
                      )}
                    </span>
                  </div>
                  <div className="flex items-center gap-2 text-xs">
                    <span className="shrink-0 text-ink-muted">优先级</span>
                    <PriorityInput
                      channel={c}
                      inputClassName="h-7 w-20 rounded-control border border-border bg-card px-2 text-right text-sm text-ink"
                    />
                  </div>
                  <div className="flex items-center gap-1 pt-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 gap-1 px-2 text-xs"
                      disabled={testing || !c.enabled || !c.models[0]}
                      loading={testing}
                      aria-label={`测试自定义模型 ${c.name}`}
                      onClick={() => {
                        setTestingId(c.id);
                        testMut.mutate({ id: c.id, model: c.models[0]?.name });
                      }}
                    >
                      测试
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={`编辑自定义模型 ${c.name}`}
                      onClick={() => setEditing(c)}
                    >
                      <Pencil className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-destructive hover:bg-destructive/10"
                      aria-label={`删除自定义模型 ${c.name}`}
                      onClick={() => setPendingDelete(c)}
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      <CustomModelEditor
        channel={editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null);
          invalidate();
        }}
      />

      <Dialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
      >
        <DialogContent variant="dialog" size="sm">
          <DialogHeader>
            <DialogTitle>删除自定义模型</DialogTitle>
            <DialogDescription>
              模型{" "}
              <span className="mono text-ink">{pendingDelete?.name}</span>{" "}
              删除后客户端将无法再命中；加入分组的引用需要先移除成员。删除后无法恢复。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="ghost" size="sm">
                取消
              </Button>
            </DialogClose>
            <Button
              variant="destructive"
              size="sm"
              loading={deleteMut.isPending}
              onClick={() => pendingDelete && deleteMut.mutate(pendingDelete.id)}
            >
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function CustomModelEditor({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel | "new" | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isNew = !channel || channel === "new";
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [modelName, setModelName] = useState("");
  const [reply, setReply] = useState("");
  const [errors, setErrors] = useState<{ name?: string; model?: string; reply?: string }>({});

  // 切换编辑目标时重置草稿；编辑态锁模型名——改模型名会整体替换渠道模型行,
  // 使分组里按 channel_model_id 引用的成员失效, 需要重建分组成员, 不在编辑器里提供。
  useEffect(() => {
    if (channel && channel !== "new") {
      setName(channel.name);
      setModelName(channel.models[0]?.name ?? "");
      setReply(channel.fixed_reply);
    } else {
      setName("");
      setModelName("");
      setReply("");
    }
    setErrors({});
  }, [channel]);

  const saveMut = useMutation({
    mutationFn: async () => {
      if (isNew) {
        return api.createChannel({
          name: name.trim(),
          type: "custom",
          enabled: true,
          is_free: false,
          builtin: false,
          base_url: "",
          key: "",
          keys: [],
          models: [{ id: 0, channel_id: 0, name: modelName.trim(), source: "manual" }],
          fixed_reply: reply,
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
        });
      }
      return api.updateChannel({
        id: channel!.id,
        name: name.trim(),
        fixed_reply: reply,
      });
    },
    // 保存前取消在途轮询 refetch：与 Channels 页 saveMut 相同的竞态防护，
    // 避免 30s 兜底轮询的旧响应在乐观更新之后返回，把保存结果覆盖掉。
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["channels"] });
    },
    onSuccess: (saved) => {
      // 保存响应即最新实体：直接替换/追加进列表缓存，界面即时更新；
      // refetch 由 onSaved 的 invalidate 兜底。
      if (saved && saved.id > 0) {
        qc.setQueryData<Channel[]>(["channels"], (prev) =>
          isNew
            ? prev
              ? [...prev, saved]
              : [saved]
            : prev
              ? prev.map((c) => (c.id === saved.id ? saved : c))
              : prev,
        );
      }
      toast.success(isNew ? "已创建" : "已保存");
      onSaved();
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  const nameError = validateField(name, NAME_RULE);
  const modelError = !modelName.trim() ? "模型名不能为空" : undefined;
  const replyError = !reply.trim() ? "固定回复不能为空" : undefined;
  const isValid = !nameError && !modelError && !replyError;

  return (
    <Dialog open={!!channel} onOpenChange={(o) => !o && onClose()}>
      <DialogContent variant="wide">
        <DialogHeader className="pr-12">
          <DialogTitle>{isNew ? "新建自定义模型" : `编辑：${name}`}</DialogTitle>
          <DialogDescription>
            客户端命中模型名即返回固定文案；模型名创建后不可修改
          </DialogDescription>
        </DialogHeader>

        {/* 全屏双栏：左 = 命中预览，右 = 表单 */}
        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          {/* ---------- 左栏：命中预览 ---------- */}
          <aside className="flex h-[32vh] min-h-0 flex-col border-b border-border md:h-auto md:w-[38%] md:border-b-0 md:border-r">
            <div className="border-b border-border px-4 py-2">
              <span className="text-xs font-medium text-ink-muted">命中预览</span>
            </div>
            <div className="flex-1 space-y-3 overflow-y-auto p-4">
              <div className="rounded-md border border-border bg-surface-subtle/30 p-3">
                <p className="mb-1.5 flex items-center gap-1.5 text-[11px] text-ink-muted">
                  <MessageSquare className="h-3 w-3" aria-hidden />
                  客户端请求
                </p>
                <p className="mono text-sm text-ink">
                  model:{" "}
                  <span className="text-primary-text">
                    {modelName.trim() || "（未填写）"}
                  </span>
                </p>
              </div>
              <div className="rounded-md border border-border bg-card/60 p-3">
                <p className="mb-1.5 flex items-center gap-1.5 text-[11px] text-ink-muted">
                  <Bot className="h-3 w-3" aria-hidden />
                  固定回复
                </p>
                <pre className="mono max-h-[40vh] overflow-y-auto whitespace-pre-wrap break-words text-sm leading-relaxed text-ink">
                  {reply.trim() || "（未填写）"}
                </pre>
              </div>
              <p className="text-[11px] leading-relaxed text-ink-subtle">
                命中该模型名时，中转不做任何上游请求，直接以固定文案合成响应
                （兼容 OpenAI Chat / Responses / Anthropic 协议，流式与非流式）。
              </p>
            </div>
          </aside>

          {/* ---------- 右栏：表单 ---------- */}
          <section className="flex min-h-0 flex-1 flex-col">
            <div className="flex-1 space-y-3 overflow-y-auto p-4">
              <Field label="名称" required error={nameError ?? undefined}>
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="例如：客服欢迎语"
                />
              </Field>
              <Field
                label="模型名"
                required
                hint="客户端请求的模型名；创建后不可修改"
                error={errors.model ?? modelError}
              >
                <Input
                  value={modelName}
                  onChange={(e) => setModelName(e.target.value)}
                  disabled={!isNew}
                  placeholder="例如：welcome-bot"
                  className={cn(!isNew && "opacity-70")}
                />
              </Field>
              <Field label="固定回复" required error={errors.reply ?? replyError}>
                <Textarea
                  value={reply}
                  onChange={(e) => setReply(e.target.value)}
                  rows={8}
                  placeholder="命中该模型时返回的固定文案"
                />
              </Field>
            </div>
          </section>
        </div>

        <DialogFooter>
          <DialogClose asChild>
            <Button variant="ghost" size="sm" onClick={onClose}>
              取消
            </Button>
          </DialogClose>
          <Button
            variant="primary"
            size="sm"
            loading={saveMut.isPending}
            disabled={!isValid}
            onClick={() => {
              const next = {
                name: validateField(name.trim(), NAME_RULE) ?? undefined,
                model: !modelName.trim() ? "模型名不能为空" : undefined,
                reply: !reply.trim() ? "固定回复不能为空" : undefined,
              };
              setErrors(next);
              if (!next.name && !next.model && !next.reply) saveMut.mutate();
            }}
          >
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
