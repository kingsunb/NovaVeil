import { useEffect, useMemo, useRef, useState } from "react";
import { Field } from "@/components/ui/field";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Eye, EyeOff, KeyRound, Plus, Search, Trash2, Pencil } from "lucide-react";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { APIKeyCreated, APIKeySummary } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { TableSkeleton } from "@/components/ui/skeleton";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { ConfirmButton } from "@/components/ui/confirm-button";
import { Input } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { EmptyState } from "@/components/ui/empty-state";
import { Switch } from "@/components/ui/switch";
import { SearchField } from "@/components/ui/search-field";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { cn, NAME_RULE, validateField, formatDatetimeLocal } from "@/lib/utils";

import {
  Dialog,
  DialogBody,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

function normalizeSupportedModels(value: string) {
  return Array.from(
    new Set(
      value
        .split(",")
        .map((model) => model.trim())
        .filter(Boolean),
    ),
  ).join(",");
}

export default function KeysPage() {
  const qc = useQueryClient();
  const [search, setSearch] = useState("");
  const [editing, setEditing] = useState<APIKeySummary | "new" | null>(null);
  const [editorNonce, setEditorNonce] = useState(0);
  const [created, setCreated] = useState<APIKeyCreated | null>(null);

  function openEditor(next: APIKeySummary | "new") {
    setEditorNonce((n) => n + 1);
    setEditing(next);
  }
  const [pendingDelete, setPendingDelete] = useState<APIKeySummary | null>(null);

  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ["keys"],
    queryFn: api.listKeys,
    // 兜底轮询：保存后的 refetch 延迟/丢失时最迟 30s 自愈。
    refetchInterval: 30_000,
  });

  const deleteMut = useMutation({
    mutationFn: (id: number) => api.deleteKey(id),
    onSuccess: () => {
      toast.success("已删除");
      setPendingDelete(null);
      qc.invalidateQueries({ queryKey: ["keys"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const filtered = useMemo(
    () =>
      (data ?? []).filter((k) => {
        if (!search) return true;
        const q = search.toLowerCase();
        return (
          k.name.toLowerCase().includes(q) ||
          (k.api_key_masked ?? "").toLowerCase().includes(q)
        );
      }),
    [data, search],
  );

  return (
    <div className="space-y-4">
      <PageToolbar
        leading={
          <SearchField
            value={search}
            onChange={setSearch}
            placeholder="搜索名称 / API 密钥…"
            aria-label="搜索密钥"
            className="w-full sm:w-auto"
            inputClassName="h-8 w-full sm:w-72 pl-8"
          />
        }
        trailing={
          <Button
            variant="primary"
            size="sm"
            className="gap-1.5"
            onClick={() => openEditor("new")}
          >
            <Plus className="h-3.5 w-3.5" aria-hidden />
            创建密钥
          </Button>
        }
      />

      {isLoading ? (
        <TableSkeleton rows={5} />
      ) : isError ? (
        <QueryErrorBanner onRetry={() => refetch()} />
      ) : (
      <Card>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs text-ink-muted">
                <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                <th scope="col" className="px-4 py-2.5 font-medium">密钥</th>
                <th scope="col" className="px-4 py-2.5 font-medium">支持模型</th>
                <th scope="col" className="px-4 py-2.5 font-medium">限流</th>
                <th scope="col" className="px-4 py-2.5 font-medium">过期</th>
                <th scope="col" className="px-4 py-2.5 font-medium">创建时间</th>
                <th scope="col" className="px-4 py-2.5 font-medium">最后使用</th>
                <th scope="col" className="px-4 py-2.5 font-medium">状态</th>
                <th scope="col" className="px-4 py-2.5 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.length === 0 ? (
                <tr>
                  <td colSpan={9}>
                    {data?.length ? (
                      <EmptyState
                        icon={<Search className="h-5 w-5" aria-hidden />}
                        title="没有匹配的密钥"
                        action={
                          <Button variant="link" size="sm" className="h-auto p-0 text-xs" onClick={() => setSearch("")}>
                            清除筛选
                          </Button>
                        }
                      />
                    ) : (
                      <EmptyState
                        icon={<KeyRound className="h-5 w-5" aria-hidden />}
                        title="还没有密钥"
                        action={
                          <Button variant="primary" size="sm" className="gap-1" onClick={() => openEditor("new")}>
                            <Plus className="h-3 w-3" aria-hidden />
                            创建第一把密钥
                          </Button>
                        }
                      />
                    )}
                  </td>
                </tr>
              ) : (
                filtered.map((k) => (
                  <tr
                    key={k.id}
                    className="border-b border-border/60 transition-colors last:border-b-0 hover:bg-surface-subtle/60"
                  >
                    <td className="px-4 py-2.5 font-medium text-ink">
                      {k.name || "未命名密钥"}
                    </td>
                    <td className="px-4 py-2.5">
                      <span
                        className="mono text-ink-muted"
                        title="完整密钥创建后不再可读；此处仅显示脱敏值"
                      >
                        {k.api_key_masked || "未配置"}
                      </span>
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        {!k.supported_models ? (
                          <Pill tone="neutral">全部</Pill>
                        ) : (
                          k.supported_models
                            .split(",")
                            .map((s) => s.trim())
                            .filter(Boolean)
                            .slice(0, 3)
                            .map((m) => (
                              <Pill key={m} tone="info">
                                {m}
                              </Pill>
                            ))
                        )}
                        {k.supported_models &&
                          k.supported_models.split(",").filter(Boolean).length > 3 && (
                            <span className="text-[11px] text-ink-muted">
                              +
                              {k.supported_models.split(",").filter(Boolean).length - 3}
                            </span>
                          )}
                      </div>
                    </td>
                    <td className="px-4 py-2.5">
                      {k.max_concurrent > 0 || k.rate_limit_rpm > 0 ? (
                        <span className="text-xs text-ink-muted">
                          {k.max_concurrent > 0 ? `并发 ${k.max_concurrent}` : "并发 ∞"}
                          {" · "}
                          {k.rate_limit_rpm > 0 ? `RPM ${k.rate_limit_rpm}` : "RPM ∞"}
                        </span>
                      ) : (
                        <span className="text-xs text-ink-subtle">不限</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      {k.expire_at ? (
                        <Pill tone="warning">
                          {new Date(k.expire_at * 1000).toLocaleString("zh-CN")}
                        </Pill>
                      ) : (
                        <Pill tone="neutral">永久</Pill>
                      )}
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-xs text-ink-muted">
                      {k.created_at
                        ? new Date(k.created_at * 1000).toLocaleString("zh-CN")
                        : "—"}
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-xs text-ink-muted">
                      {k.last_used_at
                        ? new Date(k.last_used_at * 1000).toLocaleString("zh-CN")
                        : "从未使用"}
                    </td>
                    <td className="px-4 py-2.5">
                      {k.enabled ? (
                        <Pill tone="success">启用</Pill>
                      ) : (
                        <Pill tone="neutral">停用</Pill>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 px-2 text-xs"
                          aria-label={`编辑密钥 ${k.name || "未命名"}`}
                          onClick={() => openEditor(k)}
                        >
                          <Pencil className="h-3.5 w-3.5" aria-hidden />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 px-2 text-xs text-destructive hover:bg-destructive/10"
                          aria-label={`删除密钥 ${k.name || "未命名"}`}
                          onClick={() => setPendingDelete(k)}
                        >
                          <Trash2 className="h-3.5 w-3.5" aria-hidden />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </Card>

      )}
      {/* 创建 / 编辑 */}
      <KeyEditor
        key={`${editing === "new" ? "new" : editing ? `edit-${editing.id}` : "closed"}-${editorNonce}`}
        k={editing}
        onClose={() => setEditing(null)}
        onCreated={(c) => {
          setEditing(null);
          setCreated(c);
          qc.invalidateQueries({ queryKey: ["keys"] });
        }}
        onSaved={() => {
          setEditing(null);
          qc.invalidateQueries({ queryKey: ["keys"] });
        }}
      />

      {/* 「仅此一次」展示 */}
      <Dialog
        open={!!created}
        onOpenChange={(o) => !o && setCreated(null)}
      >
        <DialogContent variant="dialog">
          <DialogHeader>
            <DialogTitle>密钥创建成功</DialogTitle>
            <DialogDescription>
              出于安全考虑，完整密钥仅在本次展示一次。请立即保存到密码管理器。
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
              离开此对话框后，将无法再次查看完整密钥。
            </div>
            <div className="mt-3 flex items-center gap-2">
              <code
                data-testid="created-key"
                className="mono flex-1 break-all rounded-md border border-border bg-surface-subtle/40 p-2.5 text-sm"
              >
                {created?.api_key}
              </code>
              <Button
                variant="secondary"
                size="sm"
                onClick={async () => {
                  if (!created?.api_key) return;
                  try {
                    await navigator.clipboard.writeText(created.api_key);
                    toast.success("已复制");
                  } catch {
                    toast.error("复制失败，请手动保存页面中的完整密钥");
                  }
                }}
              >
                <Copy className="h-3.5 w-3.5" aria-hidden />
                复制
              </Button>
            </div>
            <p className="mt-3 text-xs text-ink-muted">
              名称：<span className="text-ink">{created?.name}</span>
            </p>
          </DialogBody>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="primary" size="sm">
                我已保存
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认 */}
      <Dialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
      >
        <DialogContent variant="dialog" size="sm">
          <DialogHeader>
            <DialogTitle>删除密钥</DialogTitle>
            <DialogDescription>
              密钥 <span className="mono text-ink">{pendingDelete?.name}</span>{" "}
              删除后无法恢复，依赖此密钥的客户端将立即失效。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="ghost" size="sm">
                取消
              </Button>
            </DialogClose>
            <ConfirmButton
              tone="destructive"
              onConfirm={() =>
                pendingDelete && deleteMut.mutate(pendingDelete.id)
              }
              loading={deleteMut.isPending}
            />
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ---------------- 密钥编辑 ----------------

function KeyEditor({
  k,
  onClose,
  onCreated,
  onSaved,
}: {
  k: APIKeySummary | "new" | null;
  onClose: () => void;
  onCreated: (c: APIKeyCreated) => void;
  onSaved: () => void;
}) {
  const isNew = !k || k === "new";
  const open = !!k;

  const [name, setName] = useState(k && k !== "new" ? k.name : "");
  // 名称字段：密钥创建流程是「用户期望快点出一个能用的 key」，name 不是
  // 真正的业务约束；做成可选（覆盖 NAME_RULE.required=false），让 disabled
  // 不被 name 拖住。placeholder 与 hint 明确告诉用户留空会怎样。
  const nameRule = useMemo(() => ({ ...NAME_RULE, required: false }), []);
  const nameError = validateField(name, nameRule);
  const nameRef = useRef<HTMLInputElement | null>(null);

  // 开对话框时把焦点送到名称输入框，避免用户还要点一下
  useEffect(() => {
    if (open) {
      // 等 Dialog portal 挂载完再 focus；对话框先卸载时定时器作废（ref 已置空）。
      const t = setTimeout(() => {
        if (nameRef.current && document.contains(nameRef.current)) {
          nameRef.current.focus();
        }
      }, 30);
      return () => clearTimeout(t);
    }
  }, [open]);
  const [enabled, setEnabled] = useState(k && k !== "new" ? k.enabled : true);
  const [neverExpire, setNeverExpire] = useState(
    // expire_at?: number，0/undefined 都表示永不过期
    !(k && k !== "new" && k.expire_at),
  );
  const [expireAt, setExpireAt] = useState<string>(() => {
    if (k && k !== "new" && k.expire_at) {
      // datetime-local 输入值无时区, 浏览器按本地时间解析;
      // 用本地时间组件拼装, 避免 toISOString 的 UTC 输出在非 UTC 区域
      // 把过期时间整体平移数小时。
      return formatDatetimeLocal(k.expire_at);
    }
    return "";
  });
  const [models, setModels] = useState<string>(
    (k && k !== "new" && k.supported_models) || "",
  );
  // 密钥级限速（fail-fast 429）：输入框保留字符串态便于编辑，提交时归一；
  // 0 = 不限。
  const [maxConcurrent, setMaxConcurrent] = useState<string>(
    String((k && k !== "new" && k.max_concurrent) || 0),
  );
  const [rateLimitRPM, setRateLimitRPM] = useState<string>(
    String((k && k !== "new" && k.rate_limit_rpm) || 0),
  );
  // 自定义密钥值：创建时非空则用该值(空则后端自动生成)；编辑时非空则更新(空则保留原值)。
  const [newApiKey, setNewApiKey] = useState("");
  const {
    data: groups,
    isLoading: groupsLoading,
    isError: groupsError,
    refetch: refetchGroups,
  } = useQuery({
    queryKey: ["groups"],
    queryFn: api.listGroups,
    enabled: open,
  });
  const selectedModels = useMemo(
    () =>
      new Set(
        models
          .split(",")
          .map((model) => model.trim())
          .filter(Boolean),
      ),
    [models],
  );
  const modelOptions = useMemo(() => {
    const names = new Set((groups ?? []).map((group) => group.name));
    for (const selected of selectedModels) names.add(selected);
    return Array.from(names).sort((a, b) => a.localeCompare(b));
  }, [groups, selectedModels]);
  const expireTimestamp = neverExpire || !expireAt
    ? 0
    : Math.floor(new Date(expireAt).getTime() / 1000);
  // 用户可以选择「过去时间」创建已过期的密钥(例如紧急吊销), 这里只做格式校验,
  // 不强制未来, 避免误操作被阻断; 但若选在最近 5 分钟内, 给一次温和提示。
  const expireWarning =
    !neverExpire &&
      expireTimestamp > 0 &&
      expireTimestamp < Math.floor(Date.now() / 1000) - 300
      ? "过期时间已早于当前, 保存后密钥立即失效"
      : undefined;
  const expireError =
    !neverExpire && (!expireAt || !Number.isFinite(expireTimestamp))
      ? "请选择有效的过期时间"
      : undefined;
  const parseLimit = (value: string): number | null => {
    if (value.trim() === "") return 0;
    const parsed = Number(value);
    if (!Number.isInteger(parsed) || parsed < 0) return null;
    return parsed;
  };
  const maxConcurrentValue = parseLimit(maxConcurrent);
  const rateLimitRPMValue = parseLimit(rateLimitRPM);
  const limitError =
    maxConcurrentValue === null || rateLimitRPMValue === null
      ? "并发与 RPM 必须是非负整数"
      : undefined;
  const canSubmit = !nameError && !expireError && !limitError;

  // 密钥明文查看：列表接口只回掩码，首次点眼睛时按需直接 fetch。
  // 不走 React Query 缓存：明文只在组件 state 里存续，隐藏或关闭编辑器时
  // 立即丢弃，避免 reveal 后仍留在 query cache 中（审计 FE-02）。
  const [secretVisible, setSecretVisible] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [secretLoading, setSecretLoading] = useState(false);
  const secretFetchGenRef = useRef(0);

  async function fetchKeySecret() {
    if (isNew) return;
    const id = (k as APIKeySummary).id;
    const gen = ++secretFetchGenRef.current;
    setSecretLoading(true);
    try {
      const value = await api.getAPIKeySecret(id);
      if (gen !== secretFetchGenRef.current) return;
      setSecret(value);
    } catch (err) {
      if (gen !== secretFetchGenRef.current) return;
      setSecret(null);
      toast.error(
        err instanceof Error ? err.message : "密钥明文拉取失败",
      );
    } finally {
      if (gen === secretFetchGenRef.current) setSecretLoading(false);
    }
  }

  const toggleSecretVisible = () => {
    const next = !secretVisible;
    if (next && !isNew) {
      void fetchKeySecret();
    } else {
      // 隐藏时立即丢弃内存中的明文，不做跨眼睛/跨编辑器保留。
      setSecret(null);
    }
    setSecretVisible(next);
  };

  // 编辑器关闭（open=false）时清理明文与在途请求，防御性双保险：
  // 父级虽会用 key 强制重挂载本组件，但这里保证关闭后不依赖重挂载时序。
  useEffect(() => {
    if (open) return;
    setSecretVisible(false);
    setSecret(null);
    setSecretLoading(false);
    secretFetchGenRef.current += 1;
  }, [open]);

  function toggleModel(name: string) {
    const next = new Set(selectedModels);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setModels(Array.from(next).sort((a, b) => a.localeCompare(b)).join(","));
  }

  const createMut = useMutation({
    mutationFn: () =>
      api.createKey({
        // 名称可选：留空自动生成带时间戳的名称，避免列表里多行「未命名密钥」无法区分。
        name: name.trim() || `key-${Date.now()}`,
        api_key: newApiKey.trim(),
        enabled,
        expire_at: expireTimestamp,
        supported_models: normalizeSupportedModels(models),
        max_concurrent: maxConcurrentValue ?? 0,
        rate_limit_rpm: rateLimitRPMValue ?? 0,
      }),
    onSuccess: (c) => onCreated(c),
    onError: (e: Error) => toast.error(e.message || "创建失败"),
  });

  const updateMut = useMutation({
    mutationFn: () =>
      api.updateKey({
        id: (k as APIKeySummary).id,
        name: name.trim(),
        api_key: newApiKey.trim(),
        enabled,
        expire_at: expireTimestamp,
        supported_models: normalizeSupportedModels(models),
        max_concurrent: maxConcurrentValue ?? 0,
        rate_limit_rpm: rateLimitRPMValue ?? 0,
      }),
    onSuccess: () => onSaved(),
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  if (!open) return null;

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent variant="dialog" size="md">
        <DialogHeader>
          <DialogTitle>{isNew ? "创建密钥" : `编辑：${name}`}</DialogTitle>
          <DialogDescription>
            {isNew
              ? "密钥字符串留空由后端自动生成；名称可选（留空自动生成名称）；支持模型留空表示允许全部"
              : "密钥值留空保留原值，填写则更新；名称可选；支持模型留空表示允许全部"}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <Field
            label="名称"
            hint="可选；留空将自动生成名称"
            error={nameError ?? undefined}
          >
            <Input
              ref={nameRef}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如：ci-runner（可留空）"
              invalid={!!nameError}
            />
          </Field>

          {isNew && (
            <Field
              label="密钥值"
              hint="可选；留空由后端自动生成"
            >
              <Input
                value={newApiKey}
                onChange={(e) => setNewApiKey(e.target.value)}
                placeholder="自定义密钥字符串（可留空）"
                className="mono"
                aria-label="自定义密钥值"
              />
            </Field>
          )}

          <Field label="状态">
            <div className="flex h-8 items-center">
              <Switch checked={enabled} onCheckedChange={setEnabled} />
              <span className="ml-2 text-sm text-ink-muted">
                {enabled ? "启用" : "停用"}
              </span>
            </div>
          </Field>

          {!isNew && (
            <Field
              label="密钥值"
              hint={`列表接口只回掩码（${(k as APIKeySummary).api_key_masked || "—"}）；点眼睛按需拉取明文`}
            >
              <div className="relative">
                <Input
                  readOnly
                  type={secretVisible ? "text" : "password"}
                  // secret 未返回时给占位文案，避免 controlled→uncontrolled 闪烁
                  value={
                    secretVisible
                      ? secretLoading
                        ? "加载中…"
                        : (secret ?? "")
                      : "••••••••••••••••"
                  }
                  className="mono pr-9"
                  aria-label="API 密钥明文"
                />
                <button
                  type="button"
                  onClick={toggleSecretVisible}
                  aria-label={secretVisible ? "隐藏密钥" : "显示密钥"}
                  aria-pressed={secretVisible}
                  className="absolute right-2.5 top-1/2 -translate-y-1/2 text-ink-muted transition-colors hover:text-ink"
                >
                  {secretVisible ? (
                    <EyeOff className="h-3.5 w-3.5" aria-hidden />
                  ) : (
                    <Eye className="h-3.5 w-3.5" aria-hidden />
                  )}
                </button>
              </div>
            </Field>
          )}

          {!isNew && (
            <Field
              label="新密钥值"
              hint="可选；留空保留原值，填写则更新为新的密钥字符串"
            >
              <Input
                value={newApiKey}
                onChange={(e) => setNewApiKey(e.target.value)}
                placeholder="输入新密钥值以替换当前密钥（可留空）"
                className="mono"
                aria-label="新密钥值"
              />
            </Field>
          )}

          <Field
            label="过期时间"
            error={expireError}
            hint={
              expireError
                ? undefined
                : expireWarning ?? "永不过期，或选择一个未来时间"
            }
          >
            <div className="flex items-center gap-3">
              <label className="flex items-center gap-1.5 text-xs text-ink-muted">
                <input
                  type="checkbox"
                  checked={neverExpire}
                  onChange={(e) => setNeverExpire(e.target.checked)}
                  className="h-3.5 w-3.5 rounded border-border accent-primary"
                />
                永不过期
              </label>
              <Input
                type="datetime-local"
                disabled={neverExpire}
                value={expireAt}
                onChange={(e) => setExpireAt(e.target.value)}
                className="flex-1"
                invalid={!!expireError}
              />
            </div>
          </Field>

          <Field
            label="限流"
            error={limitError}
            hint="超限时客户端收到 429 与 Retry-After 头；0 = 不限"
          >
            <div className="grid grid-cols-2 gap-2">
              <Input
                type="number"
                min={0}
                value={maxConcurrent}
                onChange={(e) => setMaxConcurrent(e.target.value)}
                placeholder="0"
                aria-label="最大并发"
                invalid={maxConcurrentValue === null}
              />
              <Input
                type="number"
                min={0}
                value={rateLimitRPM}
                onChange={(e) => setRateLimitRPM(e.target.value)}
                placeholder="0"
                aria-label="每分钟请求数上限"
                invalid={rateLimitRPMValue === null}
              />
            </div>
            <div className="mt-1 flex gap-2 text-[11px] text-ink-subtle">
              <span>左：最大并发</span>
              <span>右：RPM / 分钟</span>
            </div>
          </Field>

          <Field
            label="支持模型"
            hint="从分组中选择下游模型；不选择表示允许全部。也可手动输入逗号分隔的名称。"
          >
            <div className="mb-2 flex flex-wrap gap-1.5">
              {groupsLoading ? (
                <span className="text-xs text-ink-muted">正在加载分组模型…</span>
              ) : groupsError ? (
                <span className="flex items-center gap-2 text-xs text-destructive">
                  分组模型加载失败
                  <Button variant="link" size="sm" className="h-auto p-0 text-xs" onClick={() => refetchGroups()}>
                    重试
                  </Button>
                </span>
              ) : modelOptions.length === 0 ? (
                <span className="text-xs text-ink-muted">暂无可选分组模型，可手动输入</span>
              ) : (
                modelOptions.map((model) => (
                  <button
                    key={model}
                    type="button"
                    onClick={() => toggleModel(model)}
                    className={cn(
                      "rounded-control border px-2 py-1 text-xs transition-colors",
                      selectedModels.has(model)
                        ? "border-primary bg-primary/10 text-primary-text"
                        : "border-border text-ink-muted hover:bg-surface-subtle hover:text-ink",
                    )}
                    aria-pressed={selectedModels.has(model)}
                  >
                    {model}
                  </button>
                ))
              )}
            </div>
            <Input
              value={models}
              onChange={(e) => setModels(e.target.value)}
              placeholder="可选：手动输入模型名，用逗号分隔"
              className="mono"
              aria-label="手动输入支持模型"
            />
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            取消
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={createMut.isPending || updateMut.isPending}
            disabled={!canSubmit}
            onClick={() => (isNew ? createMut.mutate() : updateMut.mutate())}
          >
            {isNew ? "创建" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

