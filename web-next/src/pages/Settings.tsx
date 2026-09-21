import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Field } from "@/components/ui/field";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Plus, Trash2, X, ShieldAlert, ChevronDown } from "lucide-react";

import { api, APIError, parseHeaderTemplates } from "@/lib/api";
import { useTheme } from "@/components/layout/ThemeProvider";
import { ChangePasswordForm } from "@/components/auth/ChangePasswordForm";
import { DEFAULT_TEST_MESSAGE } from "@/lib/constants";
import { validateDBDumpImport } from "@/lib/utils";
import {
  HEADER_TEMPLATES_SETTING_KEY,
  type HeaderTemplate,
  type DBImportResult,
  type GroupTestResult,
  type ProxyEntry,
  type ProxyTestResult,
} from "@/lib/types";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConfirmButton } from "@/components/ui/confirm-button";
import { Input, Textarea } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { cn, downloadJson } from "@/lib/utils";

const DEFAULT_RETENTION_DAYS = 3;
const DEFAULT_RETENTION_MAX_COUNT = 50;
const MAX_RETENTION_MAX_COUNT = 1000; // 错误最大条数硬上限, 超过则保存被拒绝

// 编译期注入的前端构建标识（vite.config define；dev 未设置时为空串）。
// 与后端 commit 并排展示在「关于」卡片，便于排查「页面跑的是哪份前端」。
const FRONTEND_BUILD_LABEL = [
  typeof __APP_VERSION__ === "string" && __APP_VERSION__ ? __APP_VERSION__ : "dev",
  typeof __APP_COMMIT__ === "string" && __APP_COMMIT__ ? __APP_COMMIT__ : "",
]
  .filter(Boolean)
  .join(" @ ");

async function _settingValue(key: string, fallback: string) {
  try {
    const setting = await api.getSetting(key);
    return setting?.value ?? fallback;
  } catch (error) {
    if (error instanceof APIError && error.status === 404) return fallback;
    throw error;
  }
}

/** 后端把 CORS 白名单存成逗号连接；编辑态按「每行一个」展示。 */
function originsFromStorage(value: string) {
  return value
    .split(",")
    .map((origin) => origin.trim())
    .filter(Boolean)
    .join("\n");
}

function originsForStorage(value: string) {
  return value
    .split(/\r?\n/)
    .map((origin) => origin.trim())
    .filter(Boolean)
    .join(",");
}

type Section =
  | "appearance"
  | "account"
  | "system"
  | "proxy-pool"
  | "header-templates"
  | "conversation"
  | "conv-trace"
  | "retention"
  | "usage-retention"
  | "tester"
  | "sync"
  | "backup"
  | "about";

const SECTIONS: { id: Section; label: string }[] = [
  { id: "appearance", label: "外观" },
  { id: "account", label: "账户" },
  { id: "system", label: "系统" },
  { id: "proxy-pool", label: "代理池" },
  { id: "header-templates", label: "Header 模板" },
  { id: "conversation", label: "对话留存" },
  { id: "conv-trace", label: "转换追踪" },
  { id: "retention", label: "错误日志保留" },
  { id: "usage-retention", label: "用量保留" },
  { id: "tester", label: "模型测试" },
  { id: "sync", label: "上游模型同步" },
  { id: "backup", label: "备份" },
  { id: "about", label: "关于" },
];

export default function SettingsPage() {
  const [active, setActive] = useState<Section>("appearance");
  return (
    <div className="grid grid-cols-1 gap-5 md:grid-cols-[196px_1fr]">
      <aside className="space-y-0.5 md:sticky md:top-4 md:self-start md:border-r md:border-border/40 md:pr-3">
        {SECTIONS.map((s) => (
          <button
            key={s.id}
            onClick={() => setActive(s.id)}
            aria-current={active === s.id ? "page" : undefined}
            className={cn(
              "flex h-9 w-full items-center rounded-control px-2.5 text-[13px] transition-colors",
              active === s.id
                ? "bg-primary/[0.10] font-medium text-primary-text shadow-[inset_2px_0_0_hsl(var(--primary))]"
                : "text-ink-muted hover:bg-surface-subtle hover:text-ink",
            )}
          >
            {s.label}
          </button>
        ))}
      </aside>
      <div>
        {active === "appearance" && <AppearanceSection />}
        {active === "account" && <AccountSection />}
        {active === "system" && <SystemSection />}
        {active === "proxy-pool" && <ProxyPoolSection />}
        {active === "header-templates" && <HeaderTemplatesSection />}
        {active === "conversation" && <ConversationSection />}
        {active === "conv-trace" && <ConvTraceSection />}
        {active === "retention" && <RetentionSection />}
        {active === "usage-retention" && <UsageRetentionSection />}
        {active === "tester" && <TesterSection />}
        {active === "sync" && <SyncSection />}
        {active === "backup" && <BackupSection />}
        {active === "about" && <AboutSection />}
      </div>
    </div>
  );
}

// ---------------- 外观 ----------------

function AppearanceSection() {
  const { theme, setTheme } = useTheme();
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>外观</CardTitle>
          <CardDescription>切换浅色 / 深色 / 跟随系统</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div>
          <p className="mb-2 text-xs font-medium text-ink-muted">主题</p>
          <SegmentedControl
            aria-label="主题"
            size="md"
            value={theme}
            onChange={setTheme}
            options={[
              { value: "light", label: "浅色" },
              { value: "dark", label: "深色" },
              { value: "system", label: "跟随系统" },
            ]}
          />
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- 账户 ----------------

function AccountSection() {
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>账户</CardTitle>
          <CardDescription>用户名与密码</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <ChangePasswordForm />
        <ChangeUsernameForm />
      </CardContent>
    </Card>
  );
}

function ChangeUsernameForm() {
  const [name, setName] = useState("");
  const mut = useMutation({
    mutationFn: () => api.changeUsername(name),
    onSuccess: () => {
      toast.success("用户名已更新");
      setName("");
    },
    onError: (e: Error) => toast.error(e.message || "修改失败"),
  });
  return (
    <div className="space-y-2 border-t border-border pt-4">
      <p className="text-xs font-medium text-ink-muted">修改用户名</p>
      <div className="flex gap-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="新用户名"
        />
        <Button
          variant="primary"
          size="sm"
          loading={mut.isPending}
          disabled={!name}
          onClick={() => mut.mutate()}
        >
          更新
        </Button>
      </div>
    </div>
  );
}

// ---------------- 系统 ----------------

function SystemSection() {
  const qc = useQueryClient();
  const { data: settings } = useQuery({
    queryKey: ["settings", "list"],
    queryFn: api.listSettings,
    // 兜底轮询：保存后的 refetch 延迟/丢失时最迟 30s 自愈。
    refetchInterval: 30_000,
  });

  const [proxy, setProxy] = useState("");
  const [cors, setCors] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [hydrated, setHydrated] = useState(false);

  // 后端 list 返回原始值（JWT 密钥除外）；首次拉到后同步进编辑态。
  useEffect(() => {
    if (!settings || hydrated) return;
    setProxy(
      settings.find((s) => s.key === "proxy_url")?.value ?? "",
    );
    setCors(
      originsFromStorage(
        settings.find((s) => s.key === "cors_allow_origins")?.value ?? "",
      ),
    );
    setModelFilter(
      settings.find((s) => s.key === "model_filter")?.value ?? "",
    );
    setHydrated(true);
  }, [settings, hydrated]);

  const saveMut = useMutation({
    mutationFn: (body: { key: string; value: string }) =>
      api.setSetting(body.key, body.value),
    onSuccess: () => {
      toast.success("已保存，实时生效");
      // OriginProtection 在管理面写入上做同源/白名单校验, 改白名单后立即生效。
      // 只 invalidate: refetch 到新值后 initialCors 更新, dirty 自然归零;
      // 之前这里额外 setHydrated(false) 会用旧缓存重灌编辑态, 把 proxy
      // 等未保存草稿一起冲掉、CORS 框回显旧值。
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  const initialProxy = settings?.find((s) => s.key === "proxy_url")?.value ?? "";
  const initialCors =
    settings?.find((s) => s.key === "cors_allow_origins")?.value ?? "";
  const initialModelFilter =
    settings?.find((s) => s.key === "model_filter")?.value ?? "";
  const proxyDirty = hydrated && proxy !== initialProxy;
  const corsDirty = hydrated && originsForStorage(cors) !== initialCors;
  const modelFilterDirty = hydrated && modelFilter !== initialModelFilter;

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>系统</CardTitle>
          <CardDescription>
            全局代理与 CORS 放行；保存后实时生效，无需重启
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <Field
          label="全局代理（可选）"
          hint="支持 http(s) 与 socks5 地址；代理密码会在日志中自动打码"
        >
          <div className="flex gap-2">
            <Input
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
              placeholder="http://user:pass@127.0.0.1:7890"
              className="mono"
            />
            <Button
              variant="primary"
              size="sm"
              loading={saveMut.isPending}
              disabled={!proxyDirty}
              aria-label="保存 全局代理"
              onClick={() =>
                saveMut.mutate({ key: "proxy_url", value: proxy.trim() })
              }
            >
              保存
            </Button>
          </div>
        </Field>
        <Field
          label="CORS 放行域名（每行一个）"
          hint="精确 origin（scheme+host），不支持 * 与裸域名；留空表示不允许跨域"
        >
          <Textarea
            value={cors}
            onChange={(e) => setCors(e.target.value)}
            placeholder={"https://app.example.com\nhttps://admin.example.com"}
            rows={4}
          />
        </Field>
        <div className="flex justify-end">
          <Button
            variant="primary"
            size="sm"
            loading={saveMut.isPending}
            disabled={!corsDirty}
            aria-label="保存 CORS 放行域名"
            onClick={() =>
              saveMut.mutate({
                key: "cors_allow_origins",
                value: originsForStorage(cors),
              })
            }
          >
            保存
          </Button>
        </div>
        <Field
          label="全局模型过滤（可选）"
          hint="ECMAScript 正则表达式，对所有渠道拉取的模型列表全局过滤，与渠道级过滤取交集。留空表示不过滤。例如 ^(gpt-4|claude|gemini) 只保留以这些前缀开头的模型"
        >
          <div className="flex gap-2">
            <Input
              value={modelFilter}
              onChange={(e) => setModelFilter(e.target.value)}
              placeholder="^(gpt-4|claude|gemini)"
              className="mono"
            />
            <Button
              variant="primary"
              size="sm"
              loading={saveMut.isPending}
              disabled={!modelFilterDirty}
              aria-label="保存 全局模型过滤"
              onClick={() =>
                saveMut.mutate({
                  key: "model_filter",
                  value: modelFilter.trim(),
                })
              }
            >
              保存
            </Button>
          </div>
        </Field>
      </CardContent>
    </Card>
  );
}

// ---------------- 代理池 ----------------

/** parseProxyPool 从设置值解析代理列表; 非法或空时返回空数组。 */
function parseProxyPool(raw?: string): ProxyEntry[] {
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr : [];
  } catch {
    return [];
  }
}

function ProxyPoolSection() {
  const qc = useQueryClient();
  const { data: settings } = useQuery({
    queryKey: ["settings", "list"],
    queryFn: api.listSettings,
    // 兜底轮询：保存后的 refetch 延迟/丢失时最迟 30s 自愈。
    refetchInterval: 30_000,
  });

  const [proxies, setProxies] = useState<ProxyEntry[]>([]);
  const [hydrated, setHydrated] = useState(false);
  // 每条代理的测试结果: id -> { ip, elapsed } | error string | "loading"
  const [testResults, setTestResults] = useState<
    Record<string, ProxyTestResult | { error: string } | "loading">
  >({});

  useEffect(() => {
    if (!settings || hydrated) return;
    setProxies(
      parseProxyPool(
        settings.find((s) => s.key === "proxy_pool")?.value,
      ),
    );
    setHydrated(true);
  }, [settings, hydrated]);

  const dirty = useMemo(() => {
    if (!hydrated || !settings) return false;
    const initial = parseProxyPool(
      settings.find((s) => s.key === "proxy_pool")?.value,
    );
    return JSON.stringify(proxies) !== JSON.stringify(initial);
  }, [proxies, settings, hydrated]);

  const saveMut = useMutation({
    mutationFn: (value: string) => api.setSetting("proxy_pool", value),
    onSuccess: () => {
      toast.success("代理池已保存");
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  function handleSave() {
    if (!dirty) return;
    for (const p of proxies) {
      if (!p.name.trim()) {
        toast.error("代理名称不能为空");
        return;
      }
      if (!p.url.trim()) {
        toast.error(`代理 ${p.name.trim()} 的地址不能为空`);
        return;
      }
    }
    saveMut.mutate(JSON.stringify(proxies));
  }

  async function handleTest(proxy: ProxyEntry) {
    if (!proxy.url.trim()) {
      toast.error("代理地址不能为空");
      return;
    }
    setTestResults((prev) => ({ ...prev, [proxy.id]: "loading" }));
    try {
      const result = await api.testProxy(proxy.url.trim());
      setTestResults((prev) => ({ ...prev, [proxy.id]: result }));
    } catch (e) {
      setTestResults((prev) => ({
        ...prev,
        [proxy.id]: { error: e instanceof Error ? e.message : "未知错误" },
      }));
    }
  }

  function addProxy() {
    setProxies((prev) => [
      ...prev,
      {
        id: crypto.randomUUID(),
        name: "",
        url: "",
        enabled: true,
      },
    ]);
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>代理池</CardTitle>
          <CardDescription>
            管理多个可选代理地址，支持 http(s) 与 socks5/socks5h；可逐条测试出口
            IP。地址支持 {"{account}"} 占位符，测试时默认以 NovaVeil 账号填充
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {proxies.length === 0 ? (
          <div className="flex h-10 items-center justify-center rounded-md border border-border bg-surface-subtle/40 text-xs text-ink-muted">
            还没有代理；点击下方「新增代理」添加
          </div>
        ) : (
          <div className="space-y-3">
            {proxies.map((proxy, idx) => {
              const result = testResults[proxy.id];
              return (
                <div
                  key={proxy.id}
                  className="space-y-2 rounded-md border border-border bg-surface-subtle/30 p-3"
                >
                  <div className="flex items-center gap-2">
                    <Input
                      value={proxy.name}
                      onChange={(e) =>
                        setProxies((prev) =>
                          prev.map((p, i) =>
                            i === idx ? { ...p, name: e.target.value } : p,
                          ),
                        )
                      }
                      placeholder="名称，如 香港-01"
                      className="flex-1"
                    />
                    <Switch
                      checked={proxy.enabled}
                      onCheckedChange={(checked) =>
                        setProxies((prev) =>
                          prev.map((p, i) =>
                            i === idx ? { ...p, enabled: checked } : p,
                          ),
                        )
                      }
                    />
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-destructive hover:bg-destructive/10"
                      onClick={() => {
                        setProxies((prev) =>
                          prev.filter((_, i) => i !== idx),
                        );
                        setTestResults((prev) => {
                          const next = { ...prev };
                          delete next[proxy.id];
                          return next;
                        });
                      }}
                      aria-label="删除代理"
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </div>
                  <div className="flex items-center gap-2">
                    <Input
                      value={proxy.url}
                      onChange={(e) =>
                        setProxies((prev) =>
                          prev.map((p, i) =>
                            i === idx ? { ...p, url: e.target.value } : p,
                          ),
                        )
                      }
                      placeholder="socks5h://user:pass@host:port"
                      className="mono flex-1 text-xs"
                    />
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={result === "loading"}
                      loading={result === "loading"}
                      onClick={() => handleTest(proxy)}
                    >
                      测试
                    </Button>
                  </div>
                  {result === "loading" && (
                    <p className="text-xs text-ink-muted">测试中…</p>
                  )}
                  {result && result !== "loading" && "ip" in result && (
                    <p className="text-xs text-success">
                      出口 IP: <span className="mono">{result.ip}</span>
                      <span className="ml-2 text-ink-muted">
                        ({result.elapsed} ms)
                      </span>
                    </p>
                  )}
                  {result && result !== "loading" && "error" in result && (
                    <p className="break-words text-xs text-destructive">
                      {result.error}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        )}

        <div className="flex items-center justify-between">
          <Button variant="ghost" size="sm" onClick={addProxy}>
            <Plus className="h-3.5 w-3.5" aria-hidden />
            新增代理
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={saveMut.isPending}
            disabled={!dirty}
            aria-label="保存 代理池"
            onClick={handleSave}
          >
            保存
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- Header 模板 ----------------

/** serializeHeaderTemplates 归一化编辑态为后端 JSON；返回 null 表示校验不通过。 */
function serializeHeaderTemplates(templates: HeaderTemplate[]): string | null {
  const cleaned: HeaderTemplate[] = [];
  for (const tpl of templates) {
    const name = tpl.name.trim();
    if (!name) return null;
    const headers = tpl.headers
      .map((h) => ({
        header_key: h.header_key.trim(),
        header_value: h.header_value,
      }))
      .filter((h) => h.header_key.length > 0);
    if (headers.length === 0) return null;
    cleaned.push({ name, headers });
  }
  return JSON.stringify(cleaned);
}

function HeaderTemplatesSection() {
  const qc = useQueryClient();
  const { data: settings } = useQuery({
    queryKey: ["settings", "list"],
    queryFn: api.listSettings,
    // 兜底轮询：保存后的 refetch 延迟/丢失时最迟 30s 自愈。
    refetchInterval: 30_000,
  });

  const [templates, setTemplates] = useState<HeaderTemplate[]>([]);
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    if (!settings || hydrated) return;
    setTemplates(
      parseHeaderTemplates(
        settings.find((s) => s.key === HEADER_TEMPLATES_SETTING_KEY)?.value,
      ),
    );
    setHydrated(true);
  }, [settings, hydrated]);

  const dirty = useMemo(() => {
    if (!hydrated || !settings) return false;
    const initial = parseHeaderTemplates(
      settings.find((s) => s.key === HEADER_TEMPLATES_SETTING_KEY)?.value,
    );
    return JSON.stringify(templates) !== JSON.stringify(initial);
  }, [templates, settings, hydrated]);

  const saveMut = useMutation({
    mutationFn: (value: string) =>
      api.setSetting(HEADER_TEMPLATES_SETTING_KEY, value),
    onSuccess: () => {
      toast.success("模板已保存");
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
    },
    // 服务端校验失败（超限/重名/非法头名）必须浮出，否则保存静默失败无从得知。
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  function handleSave() {
    if (!dirty) return;
    const names = templates.map((tpl) => tpl.name.trim());
    if (names.some((name) => !name)) {
      toast.error("模板名称不能为空");
      return;
    }
    if (new Set(names).size !== names.length) {
      toast.error("模板名称不能重复");
      return;
    }
    for (const tpl of templates) {
      if (tpl.headers.filter((h) => h.header_key.trim()).length === 0) {
        toast.error(`模板「${tpl.name.trim()}」至少需要一个 Header`);
        return;
      }
    }
    const value = serializeHeaderTemplates(templates);
    if (value === null) return;
    saveMut.mutate(value);
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Header 模板</CardTitle>
          <CardDescription>
            渠道表单「一键填充」可用的自定义请求头模板
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {templates.length === 0 ? (
          <div className="flex h-10 items-center justify-center rounded-md border border-border bg-surface-subtle/40 text-xs text-ink-muted">
            还没有模板；默认内置 codex 模板（如被删除可重新添加）
          </div>
        ) : (
          <div className="space-y-3">
            {templates.map((tpl, tplIdx) => (
              <div
                key={`tpl-${tplIdx}`}
                className="space-y-2 rounded-md border border-border bg-surface-subtle/30 p-3"
              >
                <div className="flex items-center gap-2">
                  <Input
                    value={tpl.name}
                    onChange={(e) =>
                      setTemplates((prev) =>
                        prev.map((t, i) =>
                          i === tplIdx ? { ...t, name: e.target.value } : t,
                        ),
                      )
                    }
                    placeholder="模板名称，如 codex"
                    className="flex-1"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-destructive hover:bg-destructive/10"
                    onClick={() =>
                      setTemplates((prev) =>
                        prev.filter((_, i) => i !== tplIdx),
                      )
                    }
                    aria-label="删除模板"
                  >
                    <Trash2 className="h-3.5 w-3.5" aria-hidden />
                  </Button>
                </div>
                {tpl.headers.map((header, hdrIdx) => (
                  <div key={`hdr-${hdrIdx}`} className="flex items-center gap-2">
                    <Input
                      value={header.header_key}
                      onChange={(e) =>
                        setTemplates((prev) =>
                          prev.map((t, i) =>
                            i !== tplIdx
                              ? t
                              : {
                                  ...t,
                                  headers: t.headers.map((h, j) =>
                                    j === hdrIdx
                                      ? { ...h, header_key: e.target.value }
                                      : h,
                                  ),
                                },
                          ),
                        )
                      }
                      placeholder="Header 名"
                      className="mono flex-1 text-xs"
                    />
                    <Input
                      value={header.header_value}
                      onChange={(e) =>
                        setTemplates((prev) =>
                          prev.map((t, i) =>
                            i !== tplIdx
                              ? t
                              : {
                                  ...t,
                                  headers: t.headers.map((h, j) =>
                                    j === hdrIdx
                                      ? { ...h, header_value: e.target.value }
                                      : h,
                                  ),
                                },
                          ),
                        )
                      }
                      placeholder="值"
                      className="mono flex-1 text-xs"
                    />
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() =>
                        setTemplates((prev) =>
                          prev.map((t, i) =>
                            i !== tplIdx
                              ? t
                              : {
                                  ...t,
                                  headers: t.headers.filter(
                                    (_, j) => j !== hdrIdx,
                                  ),
                                },
                          ),
                        )
                      }
                      disabled={tpl.headers.length <= 1}
                      aria-label="移除 Header"
                    >
                      <X className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </div>
                ))}
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs text-ink-muted"
                  onClick={() =>
                    setTemplates((prev) =>
                      prev.map((t, i) =>
                        i !== tplIdx
                          ? t
                          : {
                              ...t,
                              headers: [
                                ...t.headers,
                                { header_key: "", header_value: "" },
                              ],
                            },
                      ),
                    )
                  }
                >
                  <Plus className="h-3 w-3" aria-hidden />
                  添加 Header
                </Button>
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center justify-between">
          <Button
            variant="ghost"
            size="sm"
            onClick={() =>
              setTemplates((prev) => [
                ...prev,
                { name: "", headers: [{ header_key: "", header_value: "" }] },
              ])
            }
          >
            <Plus className="h-3.5 w-3.5" aria-hidden />
            新增模板
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={saveMut.isPending}
            disabled={!dirty}
            aria-label="保存 Header 模板"
            onClick={handleSave}
          >
            保存
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- 对话审计/训练留存 ----------------

function ConversationSection() {
  const qc = useQueryClient();
  const queryKey = ["conversation", "stats"];
  const { data: stats, isLoading } = useQuery({
    queryKey,
    queryFn: api.getConversationStats,
    refetchInterval: 30_000,
  });
  const { data: enabledSetting } = useQuery({
    queryKey: ["settings", "conversation_log_enabled"],
    queryFn: () =>
      api.getSetting("conversation_log_enabled").catch(swallowMissingSetting),
  });
  const { data: daysSetting } = useQuery({
    queryKey: ["settings", "conversation_retention_days"],
    queryFn: () =>
      api.getSetting("conversation_retention_days").catch(swallowMissingSetting),
  });
  const { data: capSetting } = useQuery({
    queryKey: ["settings", "conversation_dir_max_gb"],
    queryFn: () =>
      api.getSetting("conversation_dir_max_gb").catch(swallowMissingSetting),
  });

  const [enabled, setEnabled] = useState(false);
  const [days, setDays] = useState("");
  const [cap, setCap] = useState("");
  const [hydrated, setHydrated] = useState(false);
  useEffect(() => {
    if (hydrated) return;
    if (enabledSetting != null || daysSetting != null || capSetting != null) {
      if (enabledSetting) setEnabled(enabledSetting.value === "1");
      if (daysSetting) setDays(daysSetting.value || "3");
      if (capSetting) setCap(capSetting.value || "5");
      setHydrated(true);
    }
  }, [enabledSetting, daysSetting, capSetting, hydrated]);

  const saveEnabled = useMutation({
    mutationFn: () =>
      api.setSetting("conversation_log_enabled", enabled ? "1" : "0"),
    onSuccess: () => {
      toast.success("已保存,运行时立即生效");
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
      qc.invalidateQueries({ queryKey: queryKey });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  const validDays = /^\d+$/.test(days) && +days >= 1 && +days <= 365;
  const validCap = /^\d+$/.test(cap) && +cap >= 1 && +cap <= 100;
  const daysDirty = hydrated && days !== (daysSetting?.value ?? "");
  const capDirty = hydrated && cap !== (capSetting?.value ?? "");

  const saveRetention = useMutation({
    mutationFn: () =>
      Promise.all([
        api.setSetting("conversation_retention_days", days),
        api.setSetting("conversation_dir_max_gb", cap),
      ]),
    onSuccess: () => {
      toast.success("已保存,新值会在下一次清理检查时生效");
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
      qc.invalidateQueries({ queryKey: queryKey });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  const clearMut = useMutation({
    mutationFn: () => api.clearConversations(),
    onSuccess: (data) => {
      toast.success(`已清理 ${data.removed} 个归档文件`);
      qc.invalidateQueries({ queryKey: queryKey });
    },
    onError: (e: Error) => toast.error(e.message || "清理失败"),
  });

  const usagePercent = stats && stats.max_bytes > 0
    ? Math.min(100, Math.round((stats.total_bytes / stats.max_bytes) * 100))
    : 0;
  const usedGB = stats ? stats.total_bytes / (1024 ** 3) : 0;
  const maxGB = stats ? stats.max_bytes / (1024 ** 3) : 0;

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>对话留存</CardTitle>
          <CardDescription>
            完整记录终态对话(请求/响应/用量)按天写入 data/conversations,
            跨天自动 gzip 压缩; 供本地审计使用
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* 容量状态卡: 进度条 + 文件数 / 最早/最新日期 */}
        <div className="rounded-md border border-border bg-surface-subtle/40 p-3 text-xs">
          <div className="flex items-center justify-between text-ink">
            <span>
              用量 {usedGB.toFixed(2)} / {maxGB.toFixed(0)} GB ({usagePercent}%)
            </span>
            <Pill tone={stats?.enabled ? "success" : "neutral"}>
              {stats?.enabled ? "运行中" : "已停用"}
            </Pill>
          </div>
          <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-ink/[0.06]">
            <div
              className={
                usagePercent >= 90
                  ? "h-full bg-destructive"
                  : usagePercent >= 70
                    ? "h-full bg-warning"
                    : "h-full bg-primary/70"
              }
              style={{ width: `${usagePercent}%` }}
            />
          </div>
          <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-ink-muted sm:grid-cols-4">
            <span>归档 {stats?.file_count ?? 0} 个</span>
            <span>待写 {stats ? Math.round((stats.pending_bytes ?? 0) / 1024) : 0} KB</span>
            <span>累计丢弃 {stats?.dropped_total ?? 0}</span>
            <span>
              {stats?.oldest_day && stats?.newest_day
                ? `${stats.oldest_day} ~ ${stats.newest_day}`
                : "暂无归档"}
            </span>
          </div>
          {isLoading && (
            <p className="mt-2 text-ink-subtle">加载中…</p>
          )}
        </div>

        {/* 开关 */}
        <div className="flex items-center justify-between rounded-md border border-border bg-card/60 px-3 py-2">
          <div>
            <p className="text-sm font-medium text-ink">启用对话留存</p>
            <p className="text-xs text-ink-muted">
              开启后所有终态对话会落盘到 <code className="mono">{stats?.dir || "data/conversations"}</code>;
              关闭时不再写入新数据,历史归档保留
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={saveEnabled.isPending}
            />
            <Button
              variant="secondary"
              size="sm"
              loading={saveEnabled.isPending}
              disabled={!hydrated}
              aria-label="保存 对话留存开关"
              onClick={() => saveEnabled.mutate()}
            >
              保存
            </Button>
          </div>
        </div>

        {/* 保留天数 / 容量上限 */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field
            label="归档保留天数"
            hint={`到期按文件名清理(1-365); 当前值 ${stats?.retention_days ?? "—"} 天`}
          >
            <div className="flex gap-2">
              <Input
                type="number"
                min={1}
                max={365}
                value={days}
                onChange={(e) => setDays(e.target.value)}
                className="mono"
              />
            </div>
          </Field>
          <Field
            label="目录硬预算(GB)"
            hint={`目录超出后从最旧归档开始清理(1-100); 当前值 ${maxGB.toFixed(0)} GB`}
          >
            <div className="flex gap-2">
              <Input
                type="number"
                min={1}
                max={100}
                value={cap}
                onChange={(e) => setCap(e.target.value)}
                className="mono"
              />
            </div>
          </Field>
        </div>
        <div className="flex items-center justify-end gap-2">
          <ConfirmButton
            tone="destructive"
            label="清空归档"
            loadingLabel="清空中…"
            loading={clearMut.isPending}
            disabled={!stats?.file_count}
            onConfirm={() => clearMut.mutate()}
          />
          <Button
            variant="primary"
            size="sm"
            loading={saveRetention.isPending}
            disabled={!daysDirty && !capDirty}
            aria-label="保存 对话留存策略"
            onClick={() => {
              if (!validDays) {
                toast.error("保留天数必须是 1-365 之间的整数");
                return;
              }
              if (!validCap) {
                toast.error("目录预算必须是 1-100 之间的整数(GB)");
                return;
              }
              saveRetention.mutate();
            }}
          >
            保存策略
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// 把 snackbar 错误包装一层, 404 当成设置缺失让组件忽略
function swallowMissingSetting(e: unknown) {
  if (e instanceof APIError && e.status === 404) return null;
  throw e;
}

// ---------------- 转换追踪 ----------------

function ConvTraceSection() {
  const qc = useQueryClient();
  const { data: enabledSetting } = useQuery({
    queryKey: ["settings", "conv_trace_enabled"],
    queryFn: () =>
      api.getSetting("conv_trace_enabled").catch(swallowMissingSetting),
  });

  const [enabled, setEnabled] = useState(false);
  const [hydrated, setHydrated] = useState(false);
  useEffect(() => {
    if (hydrated) return;
    if (enabledSetting != null) {
      setEnabled(enabledSetting ? enabledSetting.value === "1" : false);
      setHydrated(true);
    }
  }, [enabledSetting, hydrated]);

  const saveEnabled = useMutation({
    mutationFn: () =>
      api.setSetting("conv_trace_enabled", enabled ? "1" : "0"),
    onSuccess: () => {
      toast.success("已保存,运行时立即生效");
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
      qc.invalidateQueries({ queryKey: ["settings", "conv_trace_enabled"] });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>协议转换追踪</CardTitle>
          <CardDescription>
            跨协议转换时记录耗时、请求体大小、字段降级诊断等 Debug 日志;
            关闭时仅一次缓存查询, 零额外开销
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between rounded-md border border-border bg-card/60 px-3 py-2">
          <div>
            <p className="text-sm font-medium text-ink">启用转换追踪</p>
            <p className="text-xs text-ink-muted">
              开启后跨协议请求会输出 <code className="mono">conv trace</code> 级 Debug 日志,
              包含协议对、耗时、大小变化与降级字段; 关闭时无任何 IO 与分配
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={saveEnabled.isPending}
            />
            <Button
              variant="secondary"
              size="sm"
              loading={saveEnabled.isPending}
              disabled={!hydrated}
              aria-label="保存 转换追踪开关"
              onClick={() => saveEnabled.mutate()}
            >
              保存
            </Button>
          </div>
        </div>

        <div className="rounded-md border border-border bg-surface-subtle/40 p-3 text-xs text-ink-muted">
          <p className="font-medium text-ink">性能说明</p>
          <ul className="mt-1.5 space-y-1">
            <li>• <b>关闭</b>(默认): 每次跨协议转换仅 1 次内存缓存查询, 无日志输出、无内存分配</li>
            <li>• <b>开启</b>: 额外分析请求体结构 + 转换后字段对比 + Debug 日志写入, 适合调试与排障</li>
            <li>• 日志级别为 <code className="mono">DEBUG</code>, 需 <code className="mono">NOVAVEIL_DEBUG=true</code> 才会输出到控制台</li>
          </ul>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- 日志保留 ----------------

function RetentionSection() {
  const swallowMissing = (e: unknown) =>
    e instanceof APIError && e.status === 404 ? null : Promise.reject(e);
  const { data: days } = useQuery({
    queryKey: ["setting", "error_retention_days"],
    queryFn: () => api.getSetting("error_retention_days").catch(swallowMissing),
  });
  const { data: max } = useQuery({
    queryKey: ["setting", "error_retention_max_count"],
    queryFn: () =>
      api.getSetting("error_retention_max_count").catch(swallowMissing),
  });
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>日志保留</CardTitle>
          <CardDescription>
            错误日志保留天数与最大条数；0 表示不限制
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-3">
        <RetentionField
          settingKey="error_retention_days"
          label="错误保留天数"
          value={days?.value}
          defaultValue={DEFAULT_RETENTION_DAYS}
        />
        <RetentionField
          settingKey="error_retention_max_count"
          label="错误最大条数"
          value={max?.value}
          defaultValue={DEFAULT_RETENTION_MAX_COUNT}
          maxCountLimit={MAX_RETENTION_MAX_COUNT}
        />
      </CardContent>
    </Card>
  );
}

function RetentionField({
  settingKey,
  label,
  value,
  defaultValue,
  maxCountLimit = 0, // 仅 error_retention_max_count 生效, 0 表示无上限
}: {
  settingKey: string;
  label: string;
  value?: string;
  defaultValue: number;
  maxCountLimit?: number;
}) {
  // 受控：本地编辑态用 localDraft，query 变更时用 effect 同步；缺少设置行时
  // 退回到 defaultValue，保证后端首次写入前也能保存。
  const [localDraft, setLocalDraft] = useState(
    value ?? String(defaultValue),
  );
  useEffect(() => {
    if (value != null) setLocalDraft(value);
  }, [value]);
  const valid =
    /^\d+$/.test(localDraft) && Number(localDraft) >= 0 && (maxCountLimit === 0 || Number(localDraft) <= maxCountLimit);
  const mut = useMutation({
    mutationFn: () => api.setSetting(settingKey, localDraft),
    onSuccess: () => toast.success("已保存"),
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Field label={label}>
      <div className="flex gap-2 items-center">
        <Input
          type="number"
          min={0}
          max={maxCountLimit || undefined}
          value={localDraft}
          onChange={(e) => setLocalDraft(e.target.value)}
        />
        {maxCountLimit > 0 && (
          <span className="text-xs text-ink-muted">
            最大 {maxCountLimit} 条（超过不可保存）
          </span>
        )}
        <Button
          variant="primary"
          size="sm"
          loading={mut.isPending}
          disabled={!valid}
          aria-label="保存 日志保留天数"
          onClick={() => mut.mutate()}
        >
          保存
        </Button>
      </div>
    </Field>
  );
}

// ---------------- 用量数据保留 ----------------

/**
 * UsageRetentionSection：用量分桶(趋势图 / 模型 Top / Token KPI 数据源)的保留时间。
 * 档位: 1 天 / 7 天 / 1 个月 / 3 个月 / 6 个月 / 1 年 / 3 年 / 永久; 后端校验 0 或 >= 1。
 * 保存后由每日清理任务按天删除过期分桶, 趋势/KPI 聚合同步收窄到保留窗口。
 */
function UsageRetentionSection() {
  const qc = useQueryClient();
  const { data: setting } = useQuery({
    queryKey: ["setting", "usage_retention_days"],
    queryFn: () => api.getSetting("usage_retention_days").catch(swallowMissingSetting),
  });

  const [days, setDays] = useState("0");
  const [hydrated, setHydrated] = useState(false);
  useEffect(() => {
    if (hydrated) return;
    if (setting != null) {
      setDays(setting ? setting.value || "0" : "0");
      setHydrated(true);
    }
  }, [setting, hydrated]);

  const initial = setting?.value ?? "0";
  const dirty = hydrated && days !== initial;

  const mut = useMutation({
    mutationFn: () => api.setSetting("usage_retention_days", days),
    onSuccess: () => {
      toast.success("已保存,新值会在下一次清理检查时生效");
      qc.invalidateQueries({ queryKey: ["setting", "usage_retention_days"] });
      qc.invalidateQueries({ queryKey: ["settings", "list"] });
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>用量数据保留时间</CardTitle>
          <CardDescription>
            用量分桶(趋势图 / 模型 Top / Token KPI 的数据源)的保留天数;到期数据每天清理一次
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <Field
          label="保留时间"
          hint="永久保留不清理;选择具体天数后到期按天清理最旧分桶"
        >
          <div className="flex items-center gap-2">
            <Select
              className="text-sm"
              value={days}
              onChange={(e) => setDays(e.target.value)}
            >
              <option value="1">1 天</option>
              <option value="7">7 天</option>
              <option value="30">1 个月</option>
              <option value="90">3 个月</option>
              <option value="180">6 个月</option>
              <option value="365">1 年</option>
              <option value="1095">3 年</option>
              <option value="0">永久保留</option>
            </Select>
            <Button
              variant="primary"
              size="sm"
              loading={mut.isPending}
              disabled={!dirty}
              aria-label="保存 用量保留时间"
              onClick={() => mut.mutate()}
            >
              保存
            </Button>
          </div>
        </Field>
      </CardContent>
    </Card>
  );
}

/**
 * 自定义测试消息输入：受控 + 保存到 settings.kv「channel_test_message」。
 * 渠道编辑器内的「测试连通」/「测试单个模型」/「一键全部测试」会读同一份
 * 设置项；缺省时回退到 DEFAULT_TEST_MESSAGE。
 */
function TestMessageField({
  value,
}: {
  value?: string;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState(value ?? "");
  useEffect(() => {
    setDraft(value ?? "");
  }, [value]);
  const mut = useMutation({
    mutationFn: () => api.setSetting("channel_test_message", draft.trim()),
    onSuccess: () => {
      toast.success("已保存");
      qc.invalidateQueries({ queryKey: ["setting", "channel_test_message"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Field label="测试消息">
      <div className="space-y-2">
        <Textarea
          rows={2}
          className="mono"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={DEFAULT_TEST_MESSAGE}
        />
        <div className="flex justify-end">
          <Button
            variant="primary"
            size="sm"
            loading={mut.isPending}
            aria-label="保存 测试消息"
            onClick={() => mut.mutate()}
          >
            保存
          </Button>
        </div>
      </div>
    </Field>
  );
}

// ---------------- 模型测试 ----------------

/**
 * TesterSection：选择分组后对分组内每个成员（渠道+模型）按真实路由逻辑
 * 发起连通性探测，逐成员返回连通性、延迟与回复内容。
 *  - 测试消息读 channel_test_message 设置项，缺省回退 DEFAULT_TEST_MESSAGE
 *  - 后端并发处理所有成员，前端单次请求等待结果
 */

function TesterSection() {
  const [groupId, setGroupId] = useState("");
  const { data: groups } = useQuery({
    queryKey: ["groups"],
    queryFn: api.listGroups,
  });
  const selected =
    groups?.find((g) => String(g.id) === groupId) ?? undefined;

  const [results, setResults] = useState<GroupTestResult[] | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  function toggleRow(i: number) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });
  }

  // 自定义测试消息：存到 settings.kv，缺省时回退 DEFAULT_TEST_MESSAGE。
  const { data: msgSetting } = useQuery({
    queryKey: ["setting", "channel_test_message"],
    queryFn: () =>
      api.getSetting("channel_test_message").catch((e: unknown) => {
        if (e instanceof APIError && e.status === 404) return null;
        throw e;
      }),
  });
  const testMessage = msgSetting?.value?.trim() || DEFAULT_TEST_MESSAGE;

  // 切换分组时清空旧结果
  useEffect(() => {
    setResults(null);
    setError(null);
  }, [groupId]);

  async function runTests() {
    if (!selected || running) return;
    setRunning(true);
    setResults(null);
    setError(null);
    try {
      const r = await api.testGroup(selected.id, testMessage);
      setResults(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : "unknown");
    } finally {
      setRunning(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>模型测试</CardTitle>
          <CardDescription>选分组对每个成员发起连通性探测</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <Field label="分组">
          <Select
            className="w-full text-sm"
            value={groupId}
            onChange={(e) => setGroupId(e.target.value)}
          >
            <option value="">选择分组</option>
            {groups?.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </Select>
        </Field>

        <TestMessageField value={msgSetting?.value} />

        {error && (
          <p className="rounded-md bg-destructive/10 p-2.5 text-xs break-words text-destructive">
            {error}
          </p>
        )}

        {results && results.length === 0 && (
          <p className="rounded-md bg-warning/10 p-2.5 text-xs text-warning">
            该分组没有成员；请先在「分组」标签页添加成员。
          </p>
        )}

        {results && results.length > 0 && (
          <div className="overflow-hidden rounded-md border border-border">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border bg-card/40 text-left text-xs text-ink-muted">
                  <th scope="col" className="px-3 py-1.5">渠道</th>
                  <th scope="col" className="px-3 py-1.5">模型</th>
                  <th scope="col" className="px-3 py-1.5">状态</th>
                  <th scope="col" className="px-3 py-1.5 text-right">延迟</th>
                  <th scope="col" className="px-3 py-1.5">返回内容</th>
                  <th scope="col" className="px-3 py-1.5">错误</th>
                </tr>
              </thead>
              <tbody>
                {results.map((r, i) => {
                  const hasDetail =
                    (r.status === "ok" && !!r.content) || !!r.error;
                  const isOpen = expanded.has(i);
                  return (
                    <Fragment key={`${r.channel_name}-${r.model}-${i}`}>
                      <tr
                        className={cn(
                          "border-b border-border/40 last:border-b-0",
                          hasDetail &&
                            "cursor-pointer transition-colors hover:bg-surface-subtle/50",
                        )}
                        onClick={hasDetail ? () => toggleRow(i) : undefined}
                      >
                        <td className="px-3 py-1.5 text-ink">
                          <div className="flex items-center gap-1.5">
                            {hasDetail && (
                              <ChevronDown
                                className={cn(
                                  "h-3.5 w-3.5 shrink-0 text-ink-muted transition-transform",
                                  isOpen && "rotate-180",
                                )}
                              />
                            )}
                            {r.channel_name}
                          </div>
                        </td>
                        <td className="mono px-3 py-1.5 text-ink">
                          {r.model}
                        </td>
                        <td className="px-3 py-1.5">
                          {r.status === "ok" ? (
                            <Pill tone="success">通过</Pill>
                          ) : (
                            <Pill tone="danger">失败</Pill>
                          )}
                        </td>
                        <td className="num px-3 py-1.5 text-right text-ink-muted">
                          {r.latency_ms > 0 ? `${r.latency_ms} ms` : "—"}
                        </td>
                        <td className="max-w-[200px] truncate px-3 py-1.5 text-xs text-ink-muted">
                          {r.status === "ok" && r.content ? r.content : ""}
                        </td>
                        <td className="truncate px-3 py-1.5 text-xs text-ink-muted">
                          {r.error ?? ""}
                        </td>
                      </tr>
                      {isOpen && hasDetail && (
                        <tr className="border-b border-border/40 last:border-b-0">
                          <td colSpan={6} className="bg-surface-subtle/30 px-3 py-2.5">
                            <div className="space-y-2">
                              {r.status === "ok" && r.content && (
                                <div>
                                  <p className="mb-1 text-xs font-medium text-ink-muted">
                                    返回内容
                                  </p>
                                  <pre className="max-h-48 overflow-auto rounded-md bg-card/60 p-2.5 text-xs whitespace-pre-wrap break-words text-ink">
                                    {r.content}
                                  </pre>
                                </div>
                              )}
                              {r.error && (
                                <div>
                                  <p className="mb-1 text-xs font-medium text-destructive">
                                    错误详情
                                  </p>
                                  <pre className="max-h-48 overflow-auto rounded-md bg-destructive/8 p-2.5 text-xs whitespace-pre-wrap break-words text-destructive">
                                    {r.error}
                                  </pre>
                                </div>
                              )}
                            </div>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        <div className="flex items-center gap-3">
          <Button
            variant="primary"
            size="sm"
            disabled={!selected || running}
            loading={running}
            onClick={runTests}
          >
            开始测试
          </Button>
          {running && (
            <span className="text-xs text-ink-muted">
              测试中…
            </span>
          )}
          {!running && results && results.length > 0 && (
            <span className="text-xs text-ink-muted">
              完成 · {results.filter((r) => r.status === "ok").length} / {results.length} 通过
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- 上游模型同步 ----------------

function SyncSection() {
  const { data: last } = useQuery({
    queryKey: ["channel-last-sync-time"],
    queryFn: api.lastSyncTime,
  });
  const qc = useQueryClient();
  const syncAllMut = useMutation({
    mutationFn: () => api.syncAllChannels(),
    onSuccess: () => {
      toast.success("已触发全量同步");
      qc.invalidateQueries({ queryKey: ["channel-last-sync-time"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const { data: intervalSetting } = useQuery({
    queryKey: ["setting", "sync_llm_interval"],
    queryFn: () => api.getSetting("sync_llm_interval").catch(swallowMissingSetting),
  });
  const [intervalDraft, setIntervalDraft] = useState("24");
  useEffect(() => {
    if (intervalSetting?.value) setIntervalDraft(intervalSetting.value);
  }, [intervalSetting]);
  const intervalValid =
    /^\d+$/.test(intervalDraft) &&
    Number(intervalDraft) >= 1 &&
    Number(intervalDraft) <= 8760;
  const saveIntervalMut = useMutation({
    mutationFn: () => api.setSetting("sync_llm_interval", intervalDraft),
    onSuccess: () => toast.success("已保存"),
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>上游模型同步</CardTitle>
          <CardDescription>从上游自动发现模型</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between gap-3 rounded-md border border-border bg-card/60 px-3 py-2">
          <div className="min-w-0">
            <label htmlFor="sync_llm_interval" className="text-sm font-medium text-ink">
              自动同步间隔
            </label>
            <p className="text-xs text-ink-muted">
              开启「自动同步模型」的渠道按此间隔自动拉取新模型
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Input
              id="sync_llm_interval"
              type="number"
              min={1}
              max={8760}
              value={intervalDraft}
              onChange={(event) => setIntervalDraft(event.target.value)}
              className="w-28"
            />
            <span className="text-xs text-ink-muted">小时</span>
            <Button
              variant="primary"
              size="sm"
              loading={saveIntervalMut.isPending}
              disabled={!intervalValid}
              aria-label="保存 自动同步间隔"
              onClick={() => saveIntervalMut.mutate()}
            >
              保存
            </Button>
          </div>
        </div>
        <div className="flex items-center justify-between rounded-md border border-border bg-card/60 px-3 py-2">
          <div>
            <p className="text-sm font-medium text-ink">最近一次同步</p>
            <p className="mono text-xs text-ink-muted">
              {last?.last_sync_at
                ? new Date(last.last_sync_at).toLocaleString("zh-CN")
                : "尚未同步"}
            </p>
          </div>
          <Button
            variant="primary"
            size="sm"
            loading={syncAllMut.isPending}
            onClick={() => syncAllMut.mutate()}
          >
            立即同步全部
          </Button>
        </div>
        <p className="text-xs text-ink-muted">
          单渠道同步可在「渠道」编辑面板的「模型」Tab 操作。
        </p>
      </CardContent>
    </Card>
  );
}

// ---------------- 备份 ----------------

function BackupSection() {
  const qc = useQueryClient();
  const [importSummary, setImportSummary] =
    useState<Record<string, number> | null>(null);
  const [pendingImport, setPendingImport] = useState<File | null>(null);
  // 备份文件含渠道密钥与 API Key 明文，不放进 mutation variables。
  const importFileRef = useRef<File | null>(null);

  const mut = useMutation({
    mutationFn: () => api.exportSettings(),
    onSuccess: ({ data, filename }) => {
      // 优先用服务端 Content-Disposition 建议的文件名，失败回退本地命名。
      downloadJson(filename ?? `novaveil-backup-${Date.now()}.json`, data);
      toast.success("已导出");
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const importMut = useMutation({
    mutationFn: async () => {
      const file = importFileRef.current;
      importFileRef.current = null;
      if (!file) throw new Error("未选择导入文件");
      const text = await file.text();
      const data = validateDBDumpImport(text, file.size);
      return api.importSettings(data);
    },
    onSuccess: (result: DBImportResult | undefined) => {
      toast.success("已导入");
      setPendingImport(null);
      setImportSummary(result?.rows_affected ?? null);
      // 导入会整体覆盖渠道 / 分组 / 密钥 / 设置等全部业务数据，
      // 必须失效所有受影响的查询缓存，否则各分区继续显示导入前旧值，
      // 用户基于旧值编辑会把导入覆盖回去。
      qc.invalidateQueries({ queryKey: ["channels"] });
      qc.invalidateQueries({ queryKey: ["groups"] });
      qc.invalidateQueries({ queryKey: ["keys"] });
      qc.invalidateQueries({ queryKey: ["apikeys", "secret"] });
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["setting"] });
      qc.invalidateQueries({ queryKey: ["channel-last-sync-time"] });
      qc.invalidateQueries({ queryKey: ["conversation", "stats"] });
      qc.invalidateQueries({ queryKey: ["now-version"] });
      qc.invalidateQueries({ queryKey: ["client-stats"] });
      qc.invalidateQueries({ queryKey: ["mask-config"] });
      qc.invalidateQueries({ queryKey: ["mask-rules"] });
      qc.invalidateQueries({ queryKey: ["usage-heatmap"] });
      qc.invalidateQueries({ queryKey: ["token-trends"] });
      qc.invalidateQueries({ queryKey: ["recent-errors"] });
      qc.invalidateQueries({ queryKey: ["log-errors"] });
      qc.invalidateQueries({ queryKey: ["log-stop-all-state"] });
      qc.invalidateQueries({ queryKey: ["model-eval"] });
    },
    onError: (e: Error) => toast.error(e.message || "导入失败"),
  });

  // 把 rows_affected 渲染成可读摘要（仅展示非零项，避免一长串 0）。
  const tableLabels: Record<string, string> = {
    channels: "渠道",
    channel_models: "渠道模型",
    groups: "分组",
    group_items: "分组成员",
    api_keys: "API 密钥",
    settings: "设置",
    client_stats: "客户端统计",
    usage_buckets: "用量汇总",
  };
  const summaryEntries = useMemo(() => {
    if (!importSummary) return [];
    return Object.entries(importSummary)
      .filter(([, count]) => count > 0)
      .sort((a, b) => b[1] - a[1]);
  }, [importSummary]);

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>备份</CardTitle>
          <CardDescription>
            导出 / 导入渠道、分组、密钥、设置、用量汇总与客户端统计为 JSON
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {/* 明文 Key 安全警告 */}
        <div className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/10 p-2.5 text-xs text-warning">
          <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
          <span>
            备份文件包含渠道密钥与 API Key 明文，请妥善保管（加密存储 / 限制访问权限），切勿提交到代码仓库或公共存储。
          </span>
        </div>

        <div className="flex items-center justify-between rounded-md border border-border bg-card/60 px-3 py-2">
          <div>
            <p className="text-sm font-medium text-ink">导出配置与业务数据</p>
            <p className="text-xs text-ink-muted">
              包含渠道、分组、密钥与设置项（不含日志与对话归档）
            </p>
          </div>
          <Button
            variant="secondary"
            size="sm"
            loading={mut.isPending}
            onClick={() => mut.mutate()}
          >
            导出 JSON
          </Button>
        </div>
        {/* 不用 <label> 包裹：input[type=file] 已有 aria-label，嵌套 label 会产生两个竞争标签 */}
        <div className="flex items-center justify-between rounded-md border border-border bg-card/60 px-3 py-2">
          <div>
            <p className="text-sm font-medium text-ink">导入 JSON</p>
            <p className="text-xs text-ink-muted">
              选择文件后需再次确认。导入会覆盖渠道、分组、密钥和设置（最大 1 MB）
            </p>
          </div>
          <input
            type="file"
            accept="application/json,.json"
            aria-label="选择要导入的 JSON 文件"
            onChange={(e) => {
              const f = e.target.files?.[0] ?? null;
              setImportSummary(null);
              setPendingImport(f);
              // 清空 input value，允许用户重复选同一文件。
              e.target.value = "";
            }}
            className="text-xs"
          />
        </div>
        {pendingImport && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
            <span>
              即将导入 {pendingImport.name}，会覆盖现有渠道、分组、密钥和设置。此操作不可撤销。
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={importMut.isPending}
                onClick={() => setPendingImport(null)}
              >
                取消
              </Button>
              <ConfirmButton
                tone="destructive"
                label="确认导入"
                loadingLabel="导入中…"
                loading={importMut.isPending}
                onConfirm={() => {
                  importFileRef.current = pendingImport;
                  importMut.mutate();
                }}
              />
            </div>
          </div>
        )}

        {/* 导入影响摘要 */}
        {importMut.isSuccess && importSummary && (
          <div
            data-testid="import-summary"
            className="rounded-md border border-border bg-surface-subtle/40 p-2.5 text-xs"
          >
            <p className="mb-1 font-medium text-ink">导入影响摘要</p>
            {summaryEntries.length === 0 ? (
              <p className="text-ink-muted">无数据行被修改</p>
            ) : (
              <ul className="grid grid-cols-2 gap-x-3 gap-y-0.5 text-ink-muted">
                {summaryEntries.map(([table, count]) => (
                  <li key={table} className="flex justify-between">
                    <span>{tableLabels[table] ?? table}</span>
                    <span className="num text-ink">{count}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------- 关于 ----------------

function AboutSection() {
  const qc = useQueryClient();
  const { data: now } = useQuery({
    queryKey: ["now-version"],
    queryFn: () => api.getNowVersion(),
  });
  // 调用客户端统计：按请求数倒序展示「谁在调用」，便于防滥用审计。
  const { data: clientStats, isLoading: statsLoading } = useQuery({
    queryKey: ["client-stats"],
    queryFn: api.clientStats,
  });
  const topClients = useMemo(() => {
    const list = clientStats ?? [];
    return list
      .slice()
      .sort((a, b) => b.requests - a.requests);
  }, [clientStats]);

  // 客户端统计最大保留条数设置
  const { data: maxCountSetting } = useQuery({
    queryKey: ["setting", "client_stat_max_count"],
    queryFn: () => api.getSetting("client_stat_max_count").catch(swallowMissingSetting),
  });
  const [maxCountDraft, setMaxCountDraft] = useState(
    maxCountSetting?.value ?? "10000",
  );
  useEffect(() => {
    if (maxCountSetting?.value != null) setMaxCountDraft(maxCountSetting.value);
  }, [maxCountSetting?.value]);
  const maxCountValid = /^\d+$/.test(maxCountDraft) && Number(maxCountDraft) >= 0 && Number(maxCountDraft) <= 100000;
  const saveMaxCount = useMutation({
    mutationFn: () => api.setSetting("client_stat_max_count", maxCountDraft),
    onSuccess: () => {
      toast.success("已保存");
      qc.invalidateQueries({ queryKey: ["setting", "client_stat_max_count"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader>
          <div>
            <CardTitle>NovaVeil 控制台</CardTitle>
          </div>
        </CardHeader>
        <CardContent className="space-y-1.5 text-sm text-ink-muted">
          <Row k="版本" v={now?.version} mono />
          <Row k="后端提交" v={now?.commit} mono />
          <Row k="前端构建" v={FRONTEND_BUILD_LABEL} mono />
          <Row k="构建时间" v={now?.build_time} mono />
          <Row k="调用客户端" v={now ? `${now.client_ip_count} 个 IP` : undefined} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div>
            <CardTitle>调用客户端</CardTitle>
            <CardDescription>
              按请求数排序的调用方明细，用于防滥用审计
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {/* 保留条数设置 */}
          <Field label="最大保留条数" hint="0 = 不限制；超过上限时先淘汰超过 30 天的陈旧条目，仍超限则逐出最久未调用的 IP">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={0}
                max={100000}
                value={maxCountDraft}
                onChange={(e) => setMaxCountDraft(e.target.value)}
                className="w-32"
                aria-label="客户端统计最大保留条数"
              />
              <span className="text-xs text-ink-muted">条（最多 100000）</span>
              <Button
                variant="primary"
                size="sm"
                loading={saveMaxCount.isPending}
                disabled={!maxCountValid || maxCountDraft === (maxCountSetting?.value ?? "10000")}
                aria-label="保存 客户端统计保留条数"
                onClick={() => saveMaxCount.mutate()}
              >
                保存
              </Button>
            </div>
          </Field>

          {statsLoading ? (
            <p className="py-4 text-center text-xs text-ink-muted">
              加载中…
            </p>
          ) : topClients.length === 0 ? (
            <p className="py-4 text-center text-xs text-ink-muted">
              还没有调用记录
            </p>
          ) : (
            <div className="overflow-hidden rounded-md border border-border">
              <div className="max-h-96 overflow-y-auto">
                <table className="w-full text-sm">
                  <thead className="sticky top-0 bg-card">
                    <tr className="border-b border-border bg-card/40 text-left text-xs text-ink-muted">
                      {/* 后端 ClientStat 只有 ip/first_seen/last_seen/request_count，无错误数 */}
                      <th scope="col" className="px-3 py-1.5 font-medium">客户端 IP</th>
                      <th scope="col" className="px-3 py-1.5 text-right font-medium">请求数</th>
                      <th scope="col" className="px-3 py-1.5 font-medium">最近调用</th>
                    </tr>
                  </thead>
                  <tbody>
                    {topClients.map((c) => (
                      <tr
                        key={c.client_ip}
                        className="border-b border-border/40 last:border-b-0"
                      >
                        <td className="mono px-3 py-1.5 text-ink">{c.client_ip}</td>
                        <td className="num px-3 py-1.5 text-right text-ink">
                          {formatClientCount(c.requests)}
                        </td>
                        <td className="px-3 py-1.5 text-xs text-ink-muted">
                          {formatClientLastSeen(c.last_seen)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {topClients.length > 0 && (
                <div className="border-t border-border px-3 py-1.5 text-xs text-ink-muted">
                  共 {topClients.length} 个 IP
                </div>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** formatClientCount 大数加千分位；0 保持原样。 */
function formatClientCount(value: number) {
  return value >= 10000 ? value.toLocaleString("zh-CN") : String(value);
}

/** formatClientLastSeen RFC3339 → 本地时间；零值/非法显示占位符。 */
function formatClientLastSeen(value: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() === 1) return "—";
  return date.toLocaleString("zh-CN");
}

function Row({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div className="flex items-center gap-2">
      <span>{k}</span>
      <span className={cn(mono && "mono", "text-ink")}>{v ?? "—"}</span>
    </div>
  );
}
