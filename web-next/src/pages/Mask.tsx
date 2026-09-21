/**
 * 脱敏（Mask）管理页 —— 配置请求出网前自动脱敏。
 *  - 全局总开关 + 内置规则开关 + 自定义拦截词
 *  - 脱敏测试器：输入文本预览脱敏结果与命中明细
 * 配置热生效；出厂状态恒为全关（enabled=false、规则全关、自定义词为空）。
 * 与 CustomModels.tsx 同构：useQuery/useMutation + sonner toast + ui 组件。
 */
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, ShieldCheck, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { api } from "@/lib/api";
import type { MaskConfig, MaskConfigTerm } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input, Textarea } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { Switch } from "@/components/ui/switch";
import { TableSkeleton } from "@/components/ui/skeleton";
import { QueryErrorBanner } from "@/components/ui/query-error";
import { EmptyState } from "@/components/ui/empty-state";
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

/** 自定义拦截词上限，与后端 model.MaskCustomTermLimit 对齐。 */
const TERM_LIMIT = 500;
/** 单个敏感词最大长度(字节)，与后端 model.MaxMaskCustomTermValueLen 对齐。 */
const TERM_VALUE_MAX = 200;

export default function MaskPage() {
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<number | null>(null);

  const configQ = useQuery({
    queryKey: ["mask-config"],
    queryFn: api.getMaskConfig,
  });
  const rulesQ = useQuery({
    queryKey: ["mask-rules"],
    queryFn: api.getMaskRules,
  });

  // 整份配置写回；成功后刷新配置查询，失败统一报错。
  const putMut = useMutation({
    mutationFn: (cfg: MaskConfig) => api.putMaskConfig(cfg),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["mask-config"] }),
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  const config = configQ.data;
  const rules = rulesQ.data ?? [];
  const terms: MaskConfigTerm[] = config?.custom_terms ?? [];
  const switchMap: Record<string, boolean> = config?.builtin_rule_switch ?? {};

  // 以最新 config 为底合并 patch 写回；onDone 仅在成功后执行（关弹窗/提示）。
  const put = (cfg: MaskConfig, onDone?: () => void) =>
    putMut.mutate(cfg, onDone ? { onSuccess: onDone } : undefined);
  const patch = (p: Partial<MaskConfig>) => config && put({ ...config, ...p });

  return (
    <div className="space-y-4">
      {configQ.isLoading ? (
        <div className="space-y-4">
          <TableSkeleton rows={2} />
          <TableSkeleton rows={4} />
          <TableSkeleton rows={3} />
        </div>
      ) : configQ.isError ? (
        <QueryErrorBanner onRetry={() => configQ.refetch()} />
      ) : config ? (
        <>
          {/* ---- 1. 全局总开关 ---- */}
          <Card>
            <CardHeader>
              <div className="flex items-center gap-2">
                <ShieldCheck className="h-4 w-4 text-ink-muted" aria-hidden />
                <div>
                  <CardTitle>全局总开关</CardTitle>
                  <CardDescription>
                    开启后，请求出网前自动脱敏敏感信息，模型回答时流式还原。默认关闭。
                  </CardDescription>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  checked={config.enabled}
                  disabled={putMut.isPending}
                  onCheckedChange={(v) => patch({ enabled: v })}
                />
                {config.enabled ? (
                  <Pill tone="success">已启用</Pill>
                ) : (
                  <Pill tone="neutral">已停用</Pill>
                )}
              </div>
            </CardHeader>
          </Card>

          {/* ---- 2. 内置规则 ---- */}
          <Card>
            <CardHeader>
              <div>
                <CardTitle>内置规则</CardTitle>
                <CardDescription>
                  勾选启用的内置脱敏规则；须同时开启全局总开关方可生效
                </CardDescription>
              </div>
            </CardHeader>
            <CardContent className="p-0">
              {rulesQ.isLoading ? (
                <div className="p-5 pt-0">
                  <TableSkeleton rows={4} />
                </div>
              ) : rulesQ.isError ? (
                <div className="p-5 pt-0">
                  <QueryErrorBanner onRetry={() => rulesQ.refetch()} />
                </div>
              ) : rules.length === 0 ? (
                <EmptyState
                  className="py-10"
                  icon={<ShieldCheck className="h-5 w-5" />}
                  title="暂无内置规则"
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b border-border text-left text-xs text-ink-muted">
                        <th scope="col" className="px-5 py-2.5 font-medium">规则</th>
                        <th scope="col" className="px-5 py-2.5 font-medium">说明</th>
                        <th scope="col" className="px-5 py-2.5 font-medium">启用</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rules.map((r) => {
                        const on = switchMap[r.label] ?? false;
                        return (
                          <tr
                            key={r.label}
                            className="border-b border-border/60 transition-colors last:border-b-0 hover:bg-surface-subtle/60"
                          >
                            <td className="mono px-5 py-2.5 text-ink">{r.label}</td>
                            <td className="px-5 py-2.5 text-ink-muted">
                              {r.description}
                            </td>
                            <td className="px-5 py-2.5">
                              <Switch
                                checked={on}
                                disabled={putMut.isPending}
                                onCheckedChange={(v) =>
                                  patch({
                                    builtin_rule_switch: {
                                      ...switchMap,
                                      [r.label]: v,
                                    },
                                  })
                                }
                              />
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>

          {/* ---- 3. 自定义拦截词 ---- */}
          <Card>
            <CardHeader>
              <div>
                <CardTitle>自定义拦截词</CardTitle>
                <CardDescription>
                  按词精确匹配并替换为占位符；分类仅用于管理台分组展示，不参与匹配
                </CardDescription>
              </div>
              <Button
                variant="primary"
                size="sm"
                className="gap-1.5"
                disabled={terms.length >= TERM_LIMIT || putMut.isPending}
                onClick={() => setAdding(true)}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden />
                新增
              </Button>
            </CardHeader>
            <CardContent>
              <div className="mb-3 text-xs text-ink-muted">
                共 {terms.length} 条 / 上限 {TERM_LIMIT} 条
              </div>
              {terms.length === 0 ? (
                <div className="flex flex-col items-center gap-2 py-8 text-center">
                  <p className="text-sm text-ink-muted">
                    还没有自定义拦截词；点右上角新增
                  </p>
                </div>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b border-border text-left text-xs text-ink-muted">
                        <th scope="col" className="px-4 py-2.5 font-medium">敏感词</th>
                        <th scope="col" className="px-4 py-2.5 font-medium">分类</th>
                        <th scope="col" className="px-4 py-2.5 font-medium">操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {terms.map((t, i) => (
                        <tr
                          key={`${t.value}-${i}`}
                          className="border-b border-border/60 transition-colors last:border-b-0 hover:bg-surface-subtle/60"
                        >
                          <td className="mono px-4 py-2.5 text-ink">{t.value}</td>
                          <td className="px-4 py-2.5 text-ink-muted">
                            {t.category || "—"}
                          </td>
                          <td className="px-4 py-2.5">
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7 text-destructive hover:bg-destructive/10"
                              aria-label={`删除自定义拦截词 ${t.value}`}
                              disabled={putMut.isPending}
                              onClick={() => setPendingDelete(i)}
                            >
                              <Trash2 className="h-3.5 w-3.5" aria-hidden />
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>
        </>
      ) : null}

      {/* ---- 4. 脱敏测试器（独立于配置加载） ---- */}
      <MaskTester />

      {/* 新增拦截词弹窗 */}
      <AddTermDialog
        open={adding}
        pending={putMut.isPending}
        existingValues={terms.map((t) => t.value)}
        onClose={() => setAdding(false)}
        onSubmit={(term) => {
          if (!config) return;
          put(
            { ...config, custom_terms: [...terms, term] },
            () => {
              setAdding(false);
              toast.success("已添加");
            },
          );
        }}
      />

      {/* 删除确认弹窗 */}
      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(o) => !o && setPendingDelete(null)}
      >
        <DialogContent variant="dialog" size="sm">
          <DialogHeader>
            <DialogTitle>删除自定义拦截词</DialogTitle>
            <DialogDescription>
              词{" "}
              <span className="mono text-ink">
                {pendingDelete !== null ? terms[pendingDelete]?.value : ""}
              </span>{" "}
              删除后立即生效，无法恢复。
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
              loading={putMut.isPending}
              onClick={() => {
                if (pendingDelete === null || !config) return;
                const next = terms.filter((_, i) => i !== pendingDelete);
                put({ ...config, custom_terms: next }, () => {
                  setPendingDelete(null);
                  toast.success("已删除");
                });
              }}
            >
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ---------------- 脱敏测试器 ----------------

function MaskTester() {
  const [text, setText] = useState("");
  const testMut = useMutation({
    mutationFn: (t: string) => api.testMask(t),
    onError: (e: Error) => toast.error(e.message || "测试失败"),
  });

  const result = testMut.data;
  const idle = testMut.isIdle;
  const pending = testMut.isPending;
  const isError = testMut.isError;

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>脱敏测试器</CardTitle>
          <CardDescription>
            输入文本预览脱敏结果与命中明细；仅预览，不入库不转发
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {/* 左：输入 */}
          <div className="space-y-2">
            <p className="text-xs font-medium text-ink-muted">输入文本</p>
            <Textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              rows={8}
              placeholder="粘贴待脱敏的文本…"
              className="mono"
            />
            <Button
              variant="primary"
              size="sm"
              loading={pending}
              disabled={!text.trim()}
              onClick={() => testMut.mutate(text)}
            >
              测试
            </Button>
          </div>

          {/* 右：结果 */}
          <div className="space-y-2">
            <p className="text-xs font-medium text-ink-muted">脱敏结果</p>
            {idle ? (
              <div className="flex h-32 items-center justify-center rounded-md border border-dashed border-border/60 text-xs text-ink-subtle">
                点击「测试」查看脱敏结果
              </div>
            ) : pending ? (
              <div className="flex h-32 items-center justify-center rounded-md border border-border/60 bg-surface-subtle/30 text-xs text-ink-muted">
                测试中…
              </div>
            ) : isError ? (
              <div className="flex h-32 items-center justify-center rounded-md border border-destructive/30 bg-destructive/5 text-xs text-destructive">
                测试失败，请重试
              </div>
            ) : result ? (
              <pre className="mono max-h-64 overflow-auto rounded-md bg-surface-subtle/40 p-3 text-xs">
                {result.masked || "（空）"}
              </pre>
            ) : null}

            {/* 命中明细 */}
            {result && (
              <div className="overflow-x-auto">
                {result.matches.length === 0 ? (
                  <p className="text-xs text-ink-subtle">未命中任何规则</p>
                ) : (
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="border-b border-border text-left text-ink-muted">
                        <th scope="col" className="px-2 py-2 font-medium">规则</th>
                        <th scope="col" className="px-2 py-2 font-medium">原文</th>
                        <th scope="col" className="px-2 py-2 font-medium">占位符</th>
                      </tr>
                    </thead>
                    <tbody>
                      {result.matches.map((m, i) => (
                        <tr
                          key={i}
                          className="border-b border-border/60 last:border-b-0"
                        >
                          <td className="mono px-2 py-2 text-ink">{m.label}</td>
                          <td className="mono px-2 py-2 text-ink-muted">
                            {m.original}
                          </td>
                          <td className="mono px-2 py-2 text-ink-muted">
                            {m.placeholder}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- 新增拦截词弹窗 ----------------

function AddTermDialog({
  open,
  pending,
  existingValues,
  onClose,
  onSubmit,
}: {
  open: boolean;
  pending: boolean;
  existingValues: string[];
  onClose: () => void;
  onSubmit: (term: MaskConfigTerm) => void;
}) {
  const [value, setValue] = useState("");
  const [category, setCategory] = useState("");

  // 每次打开重置草稿，避免上次输入残留。
  useEffect(() => {
    if (open) {
      setValue("");
      setCategory("");
    }
  }, [open]);

  const trimmed = value.trim();
  let valueError: string | undefined;
  if (!trimmed) {
    valueError = "敏感词不能为空";
  } else if (new Blob([trimmed]).size > TERM_VALUE_MAX) {
    valueError = `敏感词不能超过 ${TERM_VALUE_MAX} 字节`;
  } else if (existingValues.includes(trimmed)) {
    valueError = "该敏感词已存在";
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent variant="dialog">
        <DialogHeader>
          <DialogTitle>新增自定义拦截词</DialogTitle>
          <DialogDescription>
            按词精确匹配并替换为占位符；分类仅用于管理台分组展示，不参与匹配
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <Field label="敏感词" required error={valueError}>
            <Input
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="例如：张三"
            />
          </Field>
          <Field label="分类" hint="可选；仅用于管理台分组展示，不参与匹配">
            <Input
              value={category}
              onChange={(e) => setCategory(e.target.value)}
              placeholder="例如：人名"
            />
          </Field>
        </DialogBody>
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="ghost" size="sm" onClick={onClose}>
              取消
            </Button>
          </DialogClose>
          <Button
            variant="primary"
            size="sm"
            loading={pending}
            disabled={!!valueError}
            onClick={() =>
              onSubmit({ value: value.trim(), category: category.trim() })
            }
          >
            添加
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
