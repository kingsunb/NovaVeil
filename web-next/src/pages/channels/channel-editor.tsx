import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Plus,
  Trash2,
  RefreshCcw,
  X,
  FlaskConical,
  ChevronUp,
  ChevronDown,
  ChevronRight,
  Play,
  CheckCircle,
  AlertCircle,
  Loader2,
  Eye,
  EyeOff,
  WandSparkles,
  Search,
  ClipboardPaste,
} from "lucide-react";

import { api, APIError, parseHeaderTemplates } from "@/lib/api";
import type {
  Channel,
  ChannelKey,
  ChannelKeyTestResult,
  ChannelModelLimit,
  ChannelUpdateRequest,
  ProxyEntry,
} from "@/lib/types";
import { HEADER_TEMPLATES_SETTING_KEY } from "@/lib/types";
import { DEFAULT_TEST_MESSAGE } from "@/lib/constants";
import { Button } from "@/components/ui/button";


import { Input, Textarea } from "@/components/ui/input";
import { Pill } from "@/components/ui/pill";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Field } from "@/components/ui/field";
import { cn, NAME_RULE, URL_RULE, validateField } from "@/lib/utils";
import { PROVIDER_LABELS } from "./constants";
import { UpstreamProtocolLabel } from "./upstream-protocol";
import {
  Dialog,

  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogBody,
  DialogClose,
} from "@/components/ui/dialog";


type Draft = Omit<Channel, "id"> & { id?: number };

function emptyDraft(): Draft {
  return {
    name: "",
    type: "openai",
    enabled: true,
    is_free: false,
    builtin: false,
    base_url: "",
    key: "",
    // 新建渠道默认展示一个空 Key 输入行：用户可直接填写，也可留空提交
    // （后端 normalizeChannelKeys 会自动跳过空 Key 行，不会报错）。
    keys: [{ id: "", original_id: "", key: "", remark: "" }],
    models: [],
    fixed_reply: "",
    proxy: false,
    auto_sync: false,
    opencode_compat: false,
    custom_header: [],
    model_limits: {},
    tags: [],
    sort: 0,
    rate_limit_rpm: 0,
    max_concurrent: 0,
    pass_through_body_enabled: false,
  };
}

function toDraft(c: Channel | "new" | null): Draft {
  if (!c || c === "new") return emptyDraft();
  // 后端 Channel 序列化时部分切片字段（tags/keys/models/custom_header）会因
  // omitempty 在空切片时被省略；前端编辑器要稳定迭代这些字段，统一兜底为 []
  // / {}，避免 undefined.map 崩溃。
  const raw = JSON.parse(JSON.stringify(c)) as Channel;
  // 旧式单 Key 渠道：明文在 Key 列、keys 切片为空。合成一行掩码行（original_id
  // 标记 "legacy"），让列表/明文查看/逐 Key 测试/testableKeyCount 都能感知它；
  // channelKeysChanged 的对称基线保证未改动时不回写任何 keys payload。
  const synthesizedLegacyKey =
    (raw.keys ?? []).length === 0 && raw.key_masked
      ? [{ id: "", key: "", key_masked: raw.key_masked, original_id: "legacy" }]
      : [];
  return {
    ...emptyDraft(),
    ...raw,
    keys: [
      ...(raw.keys ?? []).map((key) => ({
        ...key,
        original_id: key.original_id ?? key.id,
      })),
      ...synthesizedLegacyKey,
    ],
    models: raw.models ?? [],
    tags: raw.tags ?? [],
    custom_header: raw.custom_header ?? [],
    model_limits: raw.model_limits ?? {},
  };
}

/** legacy 合成行的标记；提交侧据此把改密路由到旧式 Key 字段而不是 keys 切片。 */
const LEGACY_KEY_MARKER = "legacy";

function channelKeysChanged(original: Channel, draft: Draft) {
  // 基线与 toDraft 对称：旧式单 Key 渠道的原始 keys 为空但存在掩码，
  // 视作一行未改动的 legacy 行，避免「合成行 vs 空列表」被误判为已变更。
  const before =
    (original.keys ?? []).length > 0
      ? original.keys!
      : original.key_masked
        ? [
            {
              id: "",
              key: "",
              key_masked: original.key_masked,
              original_id: LEGACY_KEY_MARKER,
            },
          ]
        : [];
  const after = draft.keys ?? [];
  if (before.length !== after.length) return true;
  return after.some((key, index) => {
    const old = before[index];
    return (
      !old ||
      key.id !== old.id ||
      (key.key ?? "") !== "" ||
      (key.remark ?? "") !== (old.remark ?? "")
    );
  });
}

function buildChannelUpdateRequest(
  original: Channel,
  draft: Draft,
): ChannelUpdateRequest {
  const {
    key: legacyKey,
    keys: draftKeys,
    is_free: _isFree,
    builtin: _builtin,
    opencode_compat: _opencodeCompat,
    ...fields
  } = draft;
  const request = {
    ...fields,
    id: original.id,
  } as ChannelUpdateRequest;

  // 列表接口只返回掩码，空 legacyKey 表示「保留原值」，所以绝不把空字符串
  // 写回后端。只有用户明确输入新旧式单 Key 时才发送该字段。
  if (legacyKey.trim()) request.key = legacyKey.trim();

  if (channelKeysChanged(original, draft)) {
    const rows = draftKeys ?? [];
    // legacy 合成行：用户输入了新明文 → 走旧式 Key 字段整体替换（后端 req.Key
    // 路径），绝不进 keys 切片（req.Keys 是整体替换，空 key 行会被后端拒绝）。
    const typedLegacyRow = rows.find(
      (row) => row.original_id === LEGACY_KEY_MARKER && !!row.key?.trim(),
    );
    if (typedLegacyRow && rows.every((row) => row.original_id === LEGACY_KEY_MARKER)) {
      request.key = typedLegacyRow.key!.trim();
      return request;
    }
    // 未改动的 legacy 行（key 仍为空）没有明文，不能进 keys payload
    // （后端对 id+空 key 直接报错），剔除即可：req.Keys 只影响 keys 切片，
    // 旧式 Key 列保持原值。
    const payloadRows = rows.filter(
      (row) => !(row.original_id === LEGACY_KEY_MARKER && !row.key?.trim()),
    );
    if (payloadRows.length > 0) {
      // key_masked 是展示字段，不属于更新请求；id 为空交给后端按 secret 重新计算。
      request.keys = payloadRows.map(
        ({ key_masked: _masked, original_id: _originalId, ...key }) => ({
          ...key,
          key: key.key?.trim() ?? "",
        }),
      );
    }
  }
  return request;
}

export function ChannelEditor({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel | "new" | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [draft, setDraft] = useState<Draft>(() => toDraft(channel));
  // Keep a ref to the latest draft so async handlers (e.g. handleSave's auto-fetch)
  // can read the current value after an await, avoiding stale-closure data loss.
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const [tab, setTab] = useState<"cred" | "models" | "limits" | "advanced">(
    "cred",
  );
  const [newModel, setNewModel] = useState("");
  const [newTag, setNewTag] = useState("");
  // 拉取上游模型后进入「选择模式」：fetchedForSelect 暂存上游返回的全部模型，
  // fetchChecked 是用户当前勾选要保留的子集；点「确认」才合并进 draft.models，
  // 避免以前「全 auto 一个不留」的体验。
  const [fetchedForSelect, setFetchedForSelect] = useState<string[] | null>(
    null,
  );
  const [fetchChecked, setFetchChecked] = useState<Set<string>>(
    () => new Set(),
  );

  const qc = useQueryClient();
  const isNew = !channel || channel === "new";
  const open = !!channel;

  // 渠道模型测试使用的测试问题：取设置中 channel_test_message，缺失则回落默认。
  const { data: msgSetting } = useQuery({
    queryKey: ["setting", "channel_test_message"],
    queryFn: () =>
      api.getSetting("channel_test_message").catch((e: unknown) => {
        if (e instanceof APIError && e.status === 404) return null;
        throw e;
      }),
  });
  const testMessage = msgSetting?.value?.trim() || DEFAULT_TEST_MESSAGE;

  // 实时校验（仅在用户交互后显示）。编辑态只校验「用户改过的」字段：存量渠道
  // 的名称/地址可能来自导入或旧版本（如含中文括号、全角冒号等 NAME_RULE 不允许
  // 的字符，导入接口本身不做该校验），后端对它们并无此限制；若按未变更值拦截，
  // 保存按钮会一直禁用且点击毫无反馈，表现为「点保存没反应、改动不生效」。
  // 新建（isNew）或修改后的值仍走完整校验。
  const nameChanged = isNew || draft.name !== (channel as Channel).name;
  const urlChanged = isNew || draft.base_url !== (channel as Channel).base_url;
  const nameError = nameChanged ? validateField(draft.name, NAME_RULE) : null;
  const urlError = urlChanged ? validateField(draft.base_url, URL_RULE) : null;
  const isValid = !nameError && !urlError;

  // 切换 channel 重置 draft（避免上次草稿残留）。用稳定 key 判定是否真的换了
  // 编辑目标：后台 invalidateQueries 会用同一渠道的新对象引用触发本 effect，
  // 此时不能重置 —— 否则用户正在编辑、尚未保存的草稿会被静默清空（审计 §1.6）。
  const channelKey = !channel
    ? "closed"
    : channel === "new"
      ? "new"
      : `c${channel.id}`;
  const prevChannelKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (prevChannelKeyRef.current === channelKey) return;
    prevChannelKeyRef.current = channelKey;
    setDraft(toDraft(channel));
    setTab("cred");
    setFetchedForSelect(null);
    setFetchChecked(new Set());
  }, [channel, channelKey]);

  // 保存载荷含 keys[].key 明文。不能作为 mutation variables 留下：
  // React Query 会把它放进 mutation cache，登出前仍可读回。
  // 每次提交用一次性 ticket 取回载荷，mutation state 里只留数字。
  const savePayloadsRef = useRef(new Map<number, Draft>());
  const saveTicketRef = useRef(0);
  const saveMut = useMutation({
    mutationFn: (ticket: number) => {
      const d = savePayloadsRef.current.get(ticket);
      savePayloadsRef.current.delete(ticket);
      if (!d) return Promise.reject(new Error("缺少保存内容"));
      return isNew
        ? api.createChannel(d as Omit<Channel, "id">)
        : api.updateChannel(buildChannelUpdateRequest(channel as Channel, d));
    },
    // 保存前取消可能在途的列表轮询 refetch：30s 兜底轮询若恰好在保存瞬间发出，
    // 其旧响应会在 optimistic update 之后返回并覆盖「已保存」的最新结果，导致
    // 「点了保存、toast 成功，但列表数据没变」。与 Channels 页 enableMut /
    // PriorityInput 的 cancelQueries 保持一致。
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["channels"] });
    },
    onSuccess: (saved) => {
      // 保存响应即后端刷新后的最新实体：直接替换/追加进列表缓存，
      // 界面立即反映改动，不必等 invalidate 触发的 refetch 二次往返
      // （移动端经代理/CDN/隧道访问时该往返可感知地慢）。refetch 仅兜底。
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
      qc.invalidateQueries({ queryKey: ["channels"] });
      onSaved();
    },
    onError: (e: Error) => toast.error(e.message || "保存失败"),
  });

  function submitSave(next: Draft) {
    const ticket = ++saveTicketRef.current;
    savePayloadsRef.current.set(ticket, next);
    saveMut.mutate(ticket);
  }

  const fetchMut = useMutation({
    mutationFn: () => {
      // 编辑态带 id + 当前表单值：后端按表单地址请求，密钥留空回退已存凭据
      // （避免「不改密钥就必须先点眼睛拿明文」的死锁）；新建渠道 id 缺省，
      // 全部按表单值请求，支持保存前先探上游模型。
      return api.fetchModels({
        id: isNew ? undefined : channel!.id,
        type: draft.type,
        base_url: draft.base_url.trim(),
        key: draft.key.trim(),
        keys: draft.keys,
        proxy: draft.proxy,
        channel_proxy: draft.channel_proxy ?? "",
        match_regex: draft.match_regex ?? "",
        custom_header: draft.custom_header,
      });
    },
    onSuccess: (models) => {
      if (models.length === 0) {
        toast.info("上游未返回任何模型");
        return;
      }
      // 进入「选择模式」：默认全选，让用户决定哪些保留；已存在的不会出现在
      // 上游列表的过滤里，避免重复。实际可添加数 = 上游数 - 已存在数（按 name）。
      // 去重：上游偶发返回重复模型名时，Set 保证唯一，避免重复行干扰勾选状态。
      const names = Array.from(new Set(models.map((m) => m.name)));
      setFetchedForSelect(names);
      setFetchChecked(new Set(names));
    },
    onError: (e: Error) => toast.error(e.message || "拉取失败"),
  });

  function confirmFetchSelection() {
    if (!fetchedForSelect) return;
    const existing = new Set(draft.models.map((m) => m.name));
    const additions = Array.from(fetchChecked)
      .filter((n) => !existing.has(n))
      .map((name) => ({
        id: 0,
        channel_id: isNew ? 0 : channel!.id,
        name,
        source: "auto" as const,
      }));
    update("models", [...draft.models, ...additions]);
    toast.success(`已添加 ${additions.length} 个模型`);
    setFetchedForSelect(null);
    setFetchChecked(new Set());
  }

  function cancelFetchSelection() {
    setFetchedForSelect(null);
    setFetchChecked(new Set());
  }

  function toggleFetchChecked(name: string, checked: boolean) {
    setFetchChecked((prev) => {
      const next = new Set(prev);
      if (checked) next.add(name);
      else next.delete(name);
      return next;
    });
  }

  function toggleFetchAll(checked: boolean) {
    if (!fetchedForSelect) return;
    setFetchChecked(checked ? new Set(fetchedForSelect) : new Set());
  }

  // 批量勾选指定子集（搜索过滤后「全选」只作用于可见项）。
  function toggleFetchMany(names: string[], checked: boolean) {
    setFetchChecked((prev) => {
      const next = new Set(prev);
      for (const n of names) {
        if (checked) next.add(n);
        else next.delete(n);
      }
      return next;
    });
  }

  // 首次保存（新建渠道）且用户未手动拉取/添加任何模型时，后台自动拉取上游
  // 全部模型一并入库；拉取失败或上游为空时不阻塞保存，仅提示，渠道照常创建。
  const [autoFetching, setAutoFetching] = useState(false);
  async function handleSave() {
    if (isNew && draft.models.length === 0 && draft.base_url.trim()) {
      setAutoFetching(true);
      let enriched = draft;
      try {
        const models = await api.fetchModels({
          id: undefined,
          type: draft.type,
          base_url: draft.base_url.trim(),
          key: draft.key.trim(),
          keys: draft.keys,
          proxy: draft.proxy,
          channel_proxy: draft.channel_proxy ?? "",
          match_regex: draft.match_regex ?? "",
          custom_header: draft.custom_header,
        });
        if (models.length > 0) {
          // 去重：上游偶发返回重复模型名时，Set 保证唯一。
          const names = Array.from(new Set(models.map((m) => m.name)));
          enriched = {
            ...draftRef.current,
            models: names.map((name) => ({
              id: 0,
              channel_id: 0,
              name,
              source: "auto" as const,
            })),
          };
        } else {
          toast.info("上游未返回任何模型");
        }
      } catch (e) {
        toast.error(
          `自动拉取模型失败：${e instanceof Error ? e.message : "未知错误"}`,
        );
      } finally {
        setAutoFetching(false);
      }
      submitSave(enriched);
      return;
    }
    submitSave(draft);
  }

  // 单模型测试：逐个模型发起测试，结果按模型名写入 map，行内即时展示状态。
  const [modelTestResults, setModelTestResults] = useState<
    Record<string, { ok: boolean; latency_ms: number; error?: string; content?: string }>
  >({});
  const [testingModels, setTestingModels] = useState<Set<string>>(
    () => new Set(),
  );
  // 批量测试勾选集合：勾中的模型可并发小池批量探测。
  const [checkedTestModels, setCheckedTestModels] = useState<Set<string>>(
    () => new Set(),
  );

  // ---- 按密钥测试（对齐 NovaVeil 1bf0487 的 legacy 能力）----
  // testKeyID 按模型测试使用的密钥：空串 = 默认（第一把健康 Key），非空为
  // 已保存密钥行的后端 id（测试会绕过该 Key 的冷却）。
  const [testKeyID, setTestKeyID] = useState("");
  // 逐密钥测试：单次请求由后端并发测完全部密钥，结果按配置顺序展示。
  const [keyTests, setKeyTests] = useState<ChannelKeyTestResult[] | null>(null);
  const [keyTestRunning, setKeyTestRunning] = useState(false);
  const [keyTestUsedModel, setKeyTestUsedModel] = useState("");
  // 按密钥测试使用的模型：空串 = 默认第一个模型（兼容旧行为），非空为
  // 渠道模型列表中的某个模型名。
  const [keyTestModel, setKeyTestModel] = useState("");

  // savedKeyOptions 已保存（带后端 id）的密钥选择项，标签口径与后端
  // channelKeyLabel 一致（#序号(备注/ID)）；新建未保存的行没有稳定 id，
  // 无法被测试接口按 id 定位，不进入选择器。legacy 合成行同理：它代表
  // 旧式 Key 列而非一把有 id 的密钥，按 id 定位必失败——但后端「默认」
  // 本就会选到它（channelKeyTestCandidates 的回退目标），无需选择。
  const savedKeyOptions = useMemo(
    () =>
      draft.keys
        .filter((row) => row.original_id !== LEGACY_KEY_MARKER)
        .map((row, index) => ({
          id: row.original_id ?? row.id ?? "",
          label: row.remark || row.original_id || row.id || "",
          index,
        }))
        .filter((option) => option.id !== ""),
    [draft.keys],
  );
  // testableKeyCount 可测试密钥数：已保存多 Key 行 + 旧式单 Key 行（带后端掩码）。
  const testableKeyCount = useMemo(
    () =>
      draft.keys.filter((row) => !!(row.original_id ?? row.id) || !!row.key_masked)
        .length,
    [draft.keys],
  );

  // handleTestKeyChange 切换按模型测试使用的密钥：既有结果属于上一把密钥，一并清空。
  const handleTestKeyChange = (value: string) => {
    setTestKeyID(value === "default" ? "" : value);
    setModelTestResults({});
  };

  // handleKeyTest 一键核验全部密钥：后端对每把密钥各发一条测试消息（默认用
  // 用户选中的模型，未选择时回落到渠道第一个模型），逐 Key 返回有效性；
  // 单个密钥失败不影响其余密钥的判定。
  const handleKeyTest = async () => {
    if (isNew || keyTestRunning) return;
    const model = keyTestModel || draft.models[0]?.name;
    if (!model) {
      toast.warning("请先为渠道添加模型");
      return;
    }
    // 捕获当前 channelKey，await 返回后校验是否已切换渠道。
    const gen = channelKey;
    setKeyTestRunning(true);
    setKeyTests(null);
    setKeyTestUsedModel(model);
    try {
      const results = await api.testChannelKeys(channel!.id, model);
      if (gen !== prevChannelKeyRef.current) return;
      setKeyTests(results);
    } catch (err) {
      if (gen !== prevChannelKeyRef.current) return;
      toast.error(err instanceof Error ? err.message : "逐密钥测试失败");
    } finally {
      if (gen === prevChannelKeyRef.current) setKeyTestRunning(false);
    }
  };

  // 切换渠道 / 模型增删时清空旧的测试结果（避免 model 名残留误显）。
  // 与 draft 重置一样按 channelKey 判定，后台 refetch 不清空测试结果。
  // 同时中止进行中的批量测试：本组件常驻挂载（关闭 Sheet 只是 open=false），
  // 不中止会让旧渠道的计费请求继续发出、结果串写进新渠道的同名模型。
  useEffect(() => {
    modelTestsAbortedRef.current = true;
    setModelTestResults({});
    setTestingModels(new Set());
    setCheckedTestModels(new Set());
    setKeyTests(null);
    setKeyTestModel("");
  }, [channelKey]);

  // 模型列表变化时清掉已不存在的勾选, 保证「测试所选 (n)」的计数真实。
  useEffect(() => {
    setCheckedTestModels((prev) => {
      const names = draft.models.map((m) => m.name);
      const next = new Set(Array.from(prev).filter((n) => names.includes(n)));
      return next.size === prev.size ? prev : next;
    });
  }, [draft.models]);

  // 按密钥测试选中的模型被删时回退到默认（第一个模型）。
  useEffect(() => {
    if (keyTestModel && !draft.models.some((m) => m.name === keyTestModel)) {
      setKeyTestModel("");
    }
  }, [draft.models, keyTestModel]);

  // 组件卸载后中止批量测试：剩余排队项不再发往上游（每个都是真实计费请求）。
  // setup 侧复位以兼容 StrictMode 的卸载-重挂载。
  const modelTestsAbortedRef = useRef(false);
  useEffect(() => {
    modelTestsAbortedRef.current = false;
    return () => {
      modelTestsAbortedRef.current = true;
    };
  }, []);

  /** 测试单个模型；返回 Promise 供批量测试复用。 */
  async function runSingleModelTest(modelName: string) {
    if (isNew) {
      const r = { ok: false, latency_ms: 0, error: "请先保存渠道" };
      setModelTestResults((prev) => ({ ...prev, [modelName]: r }));
      return r;
    }
    setTestingModels((prev) => new Set(prev).add(modelName));
    try {
      // 指定按密钥测试选择器选中的 Key；空串由后端选第一把健康 Key。
      const result = await api.testChannel(
        channel!.id,
        modelName,
        testMessage,
        testKeyID || undefined,
      );
      // 切换渠道/卸载后不回写过期结果，避免串入新渠道界面。
      if (modelTestsAbortedRef.current) return { ok: false, latency_ms: 0, error: "aborted" };
      // 200 即成功：失败由后端以 5xx 表达，走下方 catch
      const normalized = {
        ok: true,
        latency_ms: result.latency_ms,
        error: undefined,
        content: result.content,
      };
      setModelTestResults((prev) => ({ ...prev, [modelName]: normalized }));
      return normalized;
    } catch (err) {
      if (modelTestsAbortedRef.current) return { ok: false, latency_ms: 0, error: "aborted" };
      const normalized = {
        ok: false,
        latency_ms: 0,
        error: err instanceof Error ? err.message : "测试失败",
      };
      setModelTestResults((prev) => ({ ...prev, [modelName]: normalized }));
      return normalized;
    } finally {
      setTestingModels((prev) => {
        const next = new Set(prev);
        next.delete(modelName);
        return next;
      });
    }
  }

  const handleTestModel = (modelName: string) => {
    void runSingleModelTest(modelName);
  };

  // Footer「测试连通」专用：让后端按 channel[0] 兜底模型，body 不带 model 字段。
  // 结果必须反馈：后端 30s 预算内静默返回/失败都会让管理员反复点击重试。
  const testMutForFooter = useMutation({
    mutationFn: (id: number) => api.testChannel(id, undefined, testMessage),
    onSuccess: (r) => {
      // 后端失败走 HTTP 5xx（onError），200 即成功
      toast.success(`连通 (${r.latency_ms}ms)`);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  /**
   * runModelTests 并发小池批量测试：4 个 worker 共享 FIFO 队列，刻意保守并发
   * 避免触发上游风控；卸载/切换渠道后停止派发剩余排队项。
   */
  const [testingAll, setTestingAll] = useState(false);
  async function runModelTests(models: string[]) {
    if (testingAll || models.length === 0) return;
    // 开新一轮：清掉切渠道/关弹窗置下的中止标记，否则本轮一个请求都不会发。
    modelTestsAbortedRef.current = false;
    setTestingAll(true);
    try {
      const queue = [...models];
      let okCount = 0;
      const worker = async () => {
        while (queue.length > 0) {
          if (modelTestsAbortedRef.current) return;
          const name = queue.shift();
          if (!name) break;
          const r = await runSingleModelTest(name);
          if (r.ok) okCount += 1;
        }
      };
      await Promise.all([worker(), worker(), worker(), worker()]);
      if (!modelTestsAbortedRef.current) {
        toast.success(
          `测试完成：${okCount}/${models.length} 成功，${models.length - okCount} 失败`,
        );
      }
    } finally {
      setTestingAll(false);
    }
  }

  const testAllModels = () =>
    void runModelTests(draft.models.map((m) => m.name));

  const testCheckedModels = () =>
    void runModelTests(Array.from(checkedTestModels));

  function toggleTestModelChecked(name: string, checked: boolean) {
    setCheckedTestModels((prev) => {
      const next = new Set(prev);
      if (checked) next.add(name);
      else next.delete(name);
      return next;
    });
  }

  function toggleAllTestModels(checked: boolean) {
    setCheckedTestModels(
      checked ? new Set(draft.models.map((m) => m.name)) : new Set(),
    );
  }

  if (!open) return null;

  function update<K extends keyof Draft>(k: K, v: Draft[K]) {
    setDraft((d) => ({ ...d, [k]: v }));
  }

  // 关闭编辑器时同步清理编辑态：明文 Key、眼睛状态、模型选择与批量测试都
  // 不应跨编辑目标残留。父级 state 会在 onClose 后把 channel 置 null，但这里
  // 主动清掉 draft，不依赖 effect 的滞后时序（审计 FE-02）。
  function closeEditor() {
    modelTestsAbortedRef.current = true;
    setFetchedForSelect(null);
    setFetchChecked(new Set());
    setDraft(toDraft(null));
    setTab("cred");
    onClose();
  }

  // 选择上游模型时全屏替换主编辑视图（隐藏 Tab + Footer），避免同时看到
  // 「保存/测试」与 FetchPicker 造成困惑；选择模式期间不允许 backdrop 关闭，
  // 否则会丢失勾选状态。
  const inFetchMode = fetchedForSelect != null;

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (o) return;
        // 关闭时无论是否在模型拉取选择模式都重置临时状态, 避免下次打开
        // 编辑器时残留 fetchedForSelect/fetchChecked/inFetchMode 或明文 Key。
        // 批量测试一并中止：组件不卸载，cleanup 不会触发。
        closeEditor();
      }}
    >
      <DialogContent variant="wide">
        <DialogHeader className="pr-12">
          <DialogTitle>{isNew ? "新建渠道" : `编辑：${draft.name}`}</DialogTitle>
          <DialogDescription>
            {inFetchMode
              ? "从上游拉取模型后选择要导入的项；选择模式期间请用「取消」或「确认添加」退出"
              : "凭据 · 模型 · 限制 · 高级 — 修改后点「保存」生效"}
          </DialogDescription>
        </DialogHeader>

        {/* 全屏双栏：左 = 分区导航，右 = 分区内容；拉取选择模式下导航隐藏 */}
        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          {/* ---------- 左栏：垂直分区导航 ---------- */}
          {!inFetchMode && (
            <aside className="flex shrink-0 flex-col border-b border-border md:h-auto md:w-[180px] md:border-b-0 md:border-r">
              <nav className="flex gap-1 overflow-x-auto p-2 md:flex-col md:overflow-y-auto">
                {[
                  { k: "cred", label: "凭据" },
                  { k: "models", label: `模型 (${draft.models.length})` },
                  { k: "limits", label: "限制" },
                  { k: "advanced", label: "高级" },
                ].map((t) => (
                  <button
                    key={t.k}
                    onClick={() => setTab(t.k as typeof tab)}
                    className={cn(
                      "shrink-0 rounded-control px-3 py-2 text-left text-sm transition-colors md:w-full",
                      tab === t.k
                        ? "bg-primary/10 font-medium text-primary-text"
                        : "text-ink-muted hover:bg-surface-subtle/50 hover:text-ink",
                    )}
                  >
                    {t.label}
                  </button>
                ))}
              </nav>
            </aside>
          )}

          {/* ---------- 右栏：分区内容 ---------- */}
          <section className="flex min-h-0 flex-1 flex-col">
            <div className="flex-1 overflow-y-auto p-4">
              {inFetchMode ? (
                <FetchPicker
                  fetched={fetchedForSelect}
                  checked={fetchChecked}
                  existing={new Set(draft.models.map((m) => m.name))}
                  onToggle={toggleFetchChecked}
                  onToggleAll={toggleFetchAll}
                  onToggleMany={toggleFetchMany}
                  onConfirm={confirmFetchSelection}
                  onCancel={cancelFetchSelection}
                />
              ) : (
                <>
                  {tab === "cred" && (
                    <CredTab
                      draft={draft}
                      update={update}
                      setNewTag={setNewTag}
                      newTag={newTag}
                      channelId={isNew ? undefined : channel!.id}
                      errors={{
                        name: nameError ?? undefined,
                        base_url: urlError ?? undefined,
                      }}
                    />
                  )}
                  {tab === "models" && (
                    <ModelsTab
                      draft={draft}
                      update={update}
                      newModel={newModel}
                      setNewModel={setNewModel}
                      onFetch={() => {
                        if (!draft.base_url.trim()) {
                          toast.warning("请先填写 Base URL 再拉取模型");
                          return;
                        }
                        fetchMut.mutate();
                      }}
                      fetching={fetchMut.isPending}
                      handleTestModel={handleTestModel}
                      testAllModels={testAllModels}
                      testCheckedModels={testCheckedModels}
                      toggleTestModelChecked={toggleTestModelChecked}
                      toggleAllTestModels={toggleAllTestModels}
                      checkedTestModels={checkedTestModels}
                      testingModels={testingModels}
                      testingAll={testingAll}
                      modelTestResults={modelTestResults}
                      savedKeyOptions={savedKeyOptions}
                      testKeyID={testKeyID}
                      onTestKeyChange={handleTestKeyChange}
                      testableKeyCount={testableKeyCount}
                      keyTests={keyTests}
                      keyTestRunning={keyTestRunning}
                      keyTestUsedModel={keyTestUsedModel}
                      keyTestModel={keyTestModel}
                      onKeyTestModelChange={setKeyTestModel}
                      onKeyTest={handleKeyTest}
                    />
                  )}
                  {tab === "limits" && (
                    <LimitsTab draft={draft} update={update} />
                  )}
                  {tab === "advanced" && (
                    <AdvancedTab
                      draft={draft}
                      update={update}
                    />
                  )}
                </>
              )}
            </div>
          </section>
        </div>

        {/* Footer —— 选择模式下隐藏 */}
        {!inFetchMode && (
          <DialogFooter>
            {!isNew && (
              <Button
                variant="secondary"
                size="sm"
                loading={testMutForFooter.isPending}
                onClick={() => testMutForFooter.mutate(channel!.id)}
              >
                <FlaskConical className="h-3.5 w-3.5" aria-hidden />
                测试连通
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={closeEditor}>
              取消
            </Button>
            <Button
              variant="primary"
              size="sm"
              loading={autoFetching || saveMut.isPending}
              onClick={handleSave}
              disabled={!isValid}
            >
              保存
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------- 凭据 Tab ----------------

function CredTab({
  draft,
  update,
  newTag,
  setNewTag,
  channelId,
  errors,
}: {
  draft: Draft;
  update: <K extends keyof Draft>(k: K, v: Draft[K]) => void;
  newTag: string;
  setNewTag: (v: string) => void;
  channelId: number | undefined;
  errors: { name?: string; base_url?: string };
}) {
 // 内置渠道身份字段由后端固定，前端同步禁用名称/类型/Base URL 编辑，避免提交后报错。
  const builtin = !!draft.builtin;

  // 眼睛显示：默认掩码/密文态；已保存行首次点眼睛时按需直接 fetch 该渠道的
  // 密钥明文。明文只放在本组件的 secrets 里用于展示，不写进 draft。
  // 否则点一次眼睛会让后续保存把全部明文再提交回去。
  const [visibleKeys, setVisibleKeys] = useState<Record<number, boolean>>({});
  const [secrets, setSecrets] = useState<ChannelKey[] | null>(null);
  const secretsFetchGenRef = useRef(0);
  // 用户本会话明确清空的密钥行（按稳定 id / 掩码记录）：明文查询若在此之后
  // 才返回，不再自动回填，否则用户「清空 = 删除」的意图被静默撤销（审计 §1.8）。
  const clearedSecretIdsRef = useRef<Set<string>>(new Set());
  // 批量添加密钥弹窗开关。
  const [batchOpen, setBatchOpen] = useState(false);

  async function fetchChannelKeys() {
    if (!channelId) return;
    const gen = ++secretsFetchGenRef.current;
    setSecrets(null);
    try {
      const rows = await api.getChannelKeys(channelId);
      if (gen !== secretsFetchGenRef.current) return;
      setSecrets(rows);
    } catch (err) {
      if (gen !== secretsFetchGenRef.current) return;
      setSecrets(null);
      toast.error(
        err instanceof Error ? err.message : "密钥明文拉取失败",
      );
    }
  }

  // 切换编辑目标时重置眼睛/明文/清除记录，避免上一渠道的状态泄漏到下一渠道。
  useEffect(() => {
    secretsFetchGenRef.current += 1;
    setVisibleKeys({});
    setSecrets(null);
    clearedSecretIdsRef.current = new Set();
  }, [channelId]);

  function revealedKey(row: ChannelKey): string {
    if (!secrets) return "";
    const stableId = row.original_id ?? row.id;
    if (stableId && clearedSecretIdsRef.current.has(stableId)) return "";
    if (row.key_masked && clearedSecretIdsRef.current.has(row.key_masked)) return "";
    if (row.id) return secrets.find((item) => item.id === row.id)?.key ?? "";
    if (row.key_masked) return secrets[0]?.key ?? "";
    return "";
  }

  function toggleKeyVisible(idx: number) {
    const row = draft.keys[idx];
    // 已保存行 = 带 id 的多 Key 行，或旧式单 Key 行（无 id 但有后端下发的掩码）；
    // 明文为空时首次点眼睛都触发按需拉取。新建的空白行两者皆无，不触发。
    const savedRowNeedsSecret =
      !row?.key && !!(row?.id || row?.key_masked);
    const nextVisible = !visibleKeys[idx];
    setVisibleKeys((prev) => ({ ...prev, [idx]: nextVisible }));
    if (savedRowNeedsSecret && nextVisible) void fetchChannelKeys();
  }

  function addKey() {
    const k: ChannelKey = {
      // 新 Key 的 ID 必须留空；后端按 secret 的 sha256 前 8 位生成稳定 ID。
      id: "",
      original_id: "",
      key: "",
      remark: "",
    };
    update("keys", [...draft.keys, k]);
  }
  function removeKey(idx: number) {
    update(
      "keys",
      draft.keys.filter((_, i) => i !== idx),
    );
  }
  // 批量添加密钥去重基线：本会话已输入明文的行。与这些明文重复的粘贴行在前端
  // 即跳过，避免后端 normalizeChannelKeys 的「存在重复密钥」校验让整批整体替换
  // 失败。已保存但仅持掩码的行明文未知，前端无法预判，仍由后端兜底报错。
  const existingKeySet = useMemo(
    () =>
      new Set(
        draft.keys
          .map((k) => (k.key ?? "").trim())
          .filter((k) => k.length > 0),
      ),
    [draft.keys],
  );
  function handleBatchAdd(rows: ChannelKey[]) {
    if (rows.length === 0) return;
    update("keys", [...draft.keys, ...rows]);
  }
  function addTag() {
    const t = newTag.trim();
    if (!t || draft.tags.includes(t)) return;
    update("tags", [...draft.tags, t]);
    setNewTag("");
  }
  return (
    <div className="space-y-4">
      <Field
        label="名称"
        required
        error={errors.name}
        hint={builtin ? "内置渠道名称由系统固定，不可修改" : undefined}
      >
        <Input
          value={draft.name}
          onChange={(e) => update("name", e.target.value)}
          placeholder="例如：OpenAI-Production"
          disabled={builtin}
          invalid={!!errors.name}
          aria-invalid={!!errors.name}
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="上游类型" required>
          <Select
            className="w-full text-sm"
            value={draft.type}
            disabled={builtin}
            onChange={(e) => update("type", e.target.value as Draft["type"])}
          >
            {Object.entries(PROVIDER_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="启用">
          <div className="flex h-8 items-center">
            <Switch
              checked={draft.enabled}
              onCheckedChange={(v) => update("enabled", v)}
            />
          </div>
        </Field>
      </div>
      <Field label="优先级" hint="越大越靠前，允许重复和负数">
        <Input
          type="number"
          step={1}
          value={draft.sort}
          onChange={(e) => {
            const n = Number(e.target.value);
            update("sort", Number.isFinite(n) ? Math.trunc(n) : 0);
          }}
          aria-label="优先级"
        />
      </Field>
      <Field
        label="Base URL"
        required
        error={errors.base_url}
        hint={builtin ? "内置渠道上游地址由系统固定，不可修改" : undefined}
      >
        <Input
          value={draft.base_url}
          onChange={(e) => update("base_url", e.target.value)}
          placeholder="https://api.example.com/v1"
          disabled={builtin}
          invalid={!!errors.base_url}
          aria-invalid={!!errors.base_url}
        />
      </Field>

      {/* 多 Key */}
      <div>
        <div className="mb-2 flex items-center justify-between">
          <span className="text-xs font-medium text-ink-muted">上游 Key</span>
          <div className="flex items-center gap-1.5">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setBatchOpen(true)}
            >
              <ClipboardPaste className="h-3.5 w-3.5" aria-hidden />
              批量添加
            </Button>
            <Button variant="ghost" size="sm" onClick={addKey}>
              <Plus className="h-3.5 w-3.5" aria-hidden />
              添加
            </Button>
          </div>
        </div>
        {draft.keys.length === 0 && (
          <p className="rounded-md bg-surface-subtle/60 px-3 py-2 text-xs text-ink-muted">
            尚未配置 Key；列表为空时渠道默认不需要鉴权即可使用
          </p>
        )}
        <div className="space-y-2">
          {draft.keys.map((k, i) => (
            <div key={k.id || i} className="flex items-center gap-2">
              <div className="relative flex-1">
                <Input
                  type={visibleKeys[i] ? "text" : "password"}
                  value={k.key || (visibleKeys[i] ? revealedKey(k) : "")}
                  onChange={(e) => {
                    const next = [...draft.keys];
                    const value = e.target.value;
                    const current = next[i];
                    if (!value.trim()) {
                      // 清空 = 用户要删除该密钥：记录稳定 id，明文查询晚到时不再回填。
                      const stableId = current.original_id ?? current.id;
                      if (stableId) clearedSecretIdsRef.current.add(stableId);
                      else if (current.key_masked) {
                        clearedSecretIdsRef.current.add(current.key_masked);
                      }
                    } else {
                      // 重新输入则解除「已清除」标记，恢复正常的按需回填。
                      const stableId = current.original_id ?? current.id;
                      if (stableId) clearedSecretIdsRef.current.delete(stableId);
                      if (current.key_masked) {
                        clearedSecretIdsRef.current.delete(current.key_masked);
                      }
                    }
                    next[i] = {
                      ...current,
                      key: value,
                      // 非空意味着用户要换 Secret，交给后端重新计算 ID；清空时
                      // 恢复 original_id，表示保留旧 Secret（列表不会回传明文）。
                      id: value.trim() ? "" : current.original_id ?? current.id,
                    };
                    update("keys", next);
                  }}
                  placeholder={
                    k.key_masked ? `${k.key_masked}（留空保持不变）` : "sk-…"
                  }
                  autoComplete="off"
                  className="mono pr-9"
                />
                <button
                  type="button"
                  onClick={() => toggleKeyVisible(i)}
                  aria-label={visibleKeys[i] ? "隐藏密钥" : "显示密钥"}
                  aria-pressed={!!visibleKeys[i]}
                  className="absolute right-2.5 top-1/2 -translate-y-1/2 text-ink-muted transition-colors hover:text-ink"
                >
                  {visibleKeys[i] ? (
                    <EyeOff className="h-3.5 w-3.5" aria-hidden />
                  ) : (
                    <Eye className="h-3.5 w-3.5" aria-hidden />
                  )}
                </button>
              </div>
              <Input
                value={k.remark ?? ""}
                onChange={(e) => {
                  const next = [...draft.keys];
                  next[i] = { ...next[i], remark: e.target.value };
                  update("keys", next);
                }}
                placeholder="备注"
                className="w-28"
              />
              <Button
                variant="ghost"
                size="icon"
                onClick={() => removeKey(i)}
                aria-label="删除 Key"
              >
                <X className="h-3.5 w-3.5" aria-hidden />
              </Button>
            </div>
          ))}
        </div>
      </div>

      {/* Tags */}
      <div>
        <span className="mb-2 block text-xs font-medium text-ink-muted">标签</span>
        <div className="flex flex-wrap gap-1.5">
          {draft.tags.map((t) => (
            <Pill key={t} tone="info">
              {t}
              <button
                className="ml-1 text-ink-muted hover:text-ink"
                onClick={() =>
                  update(
                    "tags",
                    draft.tags.filter((x) => x !== t),
                  )
                }
                aria-label={`移除标签 ${t}`}
              >
                <X className="h-3 w-3" aria-hidden />
              </button>
            </Pill>
          ))}
          <div className="flex items-center gap-1">
            <Input
              value={newTag}
              onChange={(e) => setNewTag(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addTag();
                }
              }}
              placeholder="+ 标签"
              className="h-6 w-24 text-xs"
            />
            <Button variant="ghost" size="sm" onClick={addTag}>
              添加
            </Button>
          </div>
        </div>
      </div>

      <BatchAddKeysDialog
        open={batchOpen}
        existingKeys={existingKeySet}
        onConfirm={(rows) => {
          handleBatchAdd(rows);
          const added = rows.length;
          toast.success(`已添加 ${added} 个密钥`);
        }}
        onClose={() => setBatchOpen(false)}
      />
    </div>
  );
}

// ---------------- 批量添加密钥弹窗 ----------------

/**
 * 批量添加密钥：一行一个密钥的粘贴入口。按行拆分、去首尾空白、丢弃空行，
 * 再对「本会话已有明文 + 本批内部重复」去重后整体追加到渠道 keys 切片。
 *
 * 仅做前端去重以提升体验；已保存但仅持掩码的行明文未知，无法预判，仍由后端
 * normalizeChannelKeys 的唯一性校验兜底（命中时整批替换失败并报错）。
 */
function BatchAddKeysDialog({
  open,
  existingKeys,
  onConfirm,
  onClose,
}: {
  open: boolean;
  existingKeys: Set<string>;
  onConfirm: (rows: ChannelKey[]) => void;
  onClose: () => void;
}) {
  const [text, setText] = useState("");
  // 每次打开清空上次残留，避免误把旧粘贴内容再次提交。
  useEffect(() => {
    if (open) setText("");
  }, [open]);

  // 实时预览：逐行解析 → 去首尾空白 → 丢弃空行 → 去重(已有/本批内部)。
  // total 含空行，skipped = 空行 + 重复行，让用户直观看到「粘贴了多少、可用多少」。
  const { rows, total, skipped } = useMemo(() => {
    const allLines = text.split(/\r?\n/);
    const seen = new Set<string>();
    const rows: ChannelKey[] = [];
    let skipped = 0;
    for (const raw of allLines) {
      const line = raw.trim();
      if (line.length === 0) {
        skipped += 1;
        continue;
      }
      if (existingKeys.has(line) || seen.has(line)) {
        skipped += 1;
        continue;
      }
      seen.add(line);
      // 新 Key 的 ID 留空：后端按 secret 的 sha256 前 8 位生成稳定 ID。
      rows.push({ id: "", original_id: "", key: line, remark: "" });
    }
    return { rows, total: allLines.length, skipped };
  }, [text, existingKeys]);

  function confirm() {
    if (rows.length === 0) return;
    onConfirm(rows);
    onClose();
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent variant="dialog" size="lg">
        <DialogHeader>
          <DialogTitle>批量添加密钥</DialogTitle>
          <DialogDescription>
            一行一个密钥，空行自动忽略；与已有密钥重复的行会自动跳过。
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="p-4 pt-3">
          <Textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={"sk-abc123...\nsk-def456...\nsk-ghi789..."}
            rows={10}
            className="mono text-sm leading-relaxed"
            aria-label="批量密钥文本"
          />
          <p className="mt-2 text-xs text-ink-muted">
            共 {total} 行 · 可添加 {rows.length} 个
            {skipped > 0 && ` · 跳过 ${skipped} 个重复/空行`}
          </p>
        </DialogBody>
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="ghost" size="sm">
              取消
            </Button>
          </DialogClose>
          <Button
            variant="primary"
            size="sm"
            onClick={confirm}
            disabled={rows.length === 0}
          >
            添加{rows.length > 0 && ` (${rows.length})`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------- 模型 Tab ----------------

/** 思考等级的可选值，与后端 model.ChannelModelLimit 注入语义对齐。 */
const THINKING_LEVEL_OPTIONS = [
  "off",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
] as const;

function ModelsTab({
  draft,
  update,
  newModel,
  setNewModel,
  onFetch,
  fetching,
  handleTestModel,
  testAllModels,
  testCheckedModels,
  toggleTestModelChecked,
  toggleAllTestModels,
  checkedTestModels,
  testingModels,
  testingAll,
  modelTestResults,
  savedKeyOptions,
  testKeyID,
  onTestKeyChange,
  testableKeyCount,
  keyTests,
  keyTestRunning,
  keyTestUsedModel,
  keyTestModel,
  onKeyTestModelChange,
  onKeyTest,
}: {
  draft: Draft;
  update: <K extends keyof Draft>(k: K, v: Draft[K]) => void;
  newModel: string;
  setNewModel: (v: string) => void;
  onFetch: () => void;
  fetching: boolean;
  handleTestModel: (modelName: string) => void;
  testAllModels: () => void;
  testCheckedModels: () => void;
  toggleTestModelChecked: (name: string, checked: boolean) => void;
  toggleAllTestModels: (checked: boolean) => void;
  checkedTestModels: Set<string>;
  testingModels: Set<string>;
  testingAll: boolean;
  modelTestResults: Record<string, { ok: boolean; latency_ms: number; error?: string; content?: string }>;
  savedKeyOptions: Array<{ id: string; label: string; index: number }>;
  testKeyID: string;
  onTestKeyChange: (value: string) => void;
  testableKeyCount: number;
  keyTests: ChannelKeyTestResult[] | null;
  keyTestRunning: boolean;
  keyTestUsedModel: string;
  keyTestModel: string;
  onKeyTestModelChange: (value: string) => void;
  onKeyTest: () => void;
}) {
  // 手风琴：当前展开配置的模型名；一次只展开一行，保持列表可扫读。
  const [expandedModel, setExpandedModel] = useState<string | null>(null);
  // 模型列表搜索过滤：只影响展示，不动勾选/上移/下移/移除（那些基于全量 draft.models 与原始索引）。
  const [modelSearch, setModelSearch] = useState("");
  const modelQuery = modelSearch.trim().toLowerCase();
  const visibleModels = modelQuery
    ? draft.models
        .map((m, i) => ({ m, i }))
        .filter(({ m }) => m.name.toLowerCase().includes(modelQuery))
    : draft.models.map((m, i) => ({ m, i }));

  function add() {
    const m = newModel.trim();
    if (!m) return;
    if (draft.models.some((x) => x.name === m)) {
      toast.warning("模型已存在");
      return;
    }
    update("models", [
      ...draft.models,
      {
        id: 0,
        channel_id: draft.id ?? 0,
        name: m,
        source: "manual",
      },
    ]);
    setNewModel("");
  }

  /** 更新单个模型的限制配置；全空时删除条目，避免给后端塞无意义的空对象。 */
  function setModelLimit(name: string, patch: Partial<ChannelModelLimit>) {
    const current = draft.model_limits[name] ?? {};
    const merged = { ...current, ...patch };
    const limits = { ...draft.model_limits };
    const empty =
      (merged.max_output == null || merged.max_output === 0) &&
      !merged.thinking_level;
    if (empty) delete limits[name];
    else limits[name] = merged;
    update("model_limits", limits);
  }

  const allChecked =
    draft.models.length > 0 &&
    draft.models.every((m) => checkedTestModels.has(m.name));
  const someChecked = draft.models.some((m) => checkedTestModels.has(m.name));

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between rounded-md border border-border bg-surface-subtle/40 p-3">
        <div>
          <p className="text-sm font-medium text-ink">从上游拉取模型</p>
          <p className="text-xs text-ink-muted">
            新建渠道填好地址即可拉取；编辑态未重填密钥时自动复用已存凭据
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={onFetch}
            loading={fetching}
          >
            <RefreshCcw className="h-3.5 w-3.5" aria-hidden />
            拉取
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={testAllModels}
            loading={testingAll}
            disabled={draft.models.length === 0}
            title="并发测试全部模型"
          >
            <Play className="h-3.5 w-3.5" aria-hidden />
            全部测试
          </Button>
        </div>
      </div>

      {/* 按密钥测试工具行：仅存在 ≥2 把已保存 Key 时提供按 Key 选择；
          「按密钥测试」由后端对每把 Key 并发各发一条测试消息。 */}
      {(savedKeyOptions.length >= 2 || testableKeyCount > 0) && (
        <div className="flex flex-wrap items-center justify-end gap-1.5">
          {savedKeyOptions.length >= 2 && (
            <Select
              value={testKeyID || "default"}
              onChange={(e) => onTestKeyChange(e.target.value)}
              aria-label="按模型测试使用的密钥"
              className="h-7 text-xs"
            >
              <option value="default">默认（第一把健康 Key）</option>
              {savedKeyOptions.map((option) => (
                <option key={option.id} value={option.id}>
                  {`#${option.index + 1}(${option.label})`}
                </option>
              ))}
            </Select>
          )}
          {draft.models.length >= 2 && (
            <Select
              value={keyTestModel}
              onChange={(e) => onKeyTestModelChange(e.target.value)}
              aria-label="按密钥测试使用的模型"
              className="h-7 text-xs"
            >
              <option value="">默认（第一个模型）</option>
              {draft.models.map((m) => (
                <option key={m.name} value={m.name}>
                  {m.name}
                </option>
              ))}
            </Select>
          )}
          <Button
            variant="secondary"
            size="sm"
            className="h-7 px-2 text-xs"
            onClick={onKeyTest}
            loading={keyTestRunning}
            disabled={testableKeyCount === 0 || draft.models.length === 0}
            title="对每把密钥各发送一条测试消息，逐 Key 展示可用性"
          >
            <FlaskConical className="h-3 w-3" aria-hidden />
            按密钥测试 ({testableKeyCount})
          </Button>
        </div>
      )}
      {keyTests && (
        <div className="space-y-1.5 rounded-md border border-border bg-background p-2">
          <p className="text-xs text-ink-muted">
            逐密钥测试结果 · 模型 {keyTestUsedModel}
          </p>
          {keyTests.map((item, index) => (
            <div
              key={item.key_id || `key-test-${index}`}
              className="flex flex-wrap items-center gap-1.5 text-xs"
            >
              <Pill tone={item.ok ? "success" : "danger"}>
                {item.ok ? "可用" : "失败"}
              </Pill>
              <span className="shrink-0 font-medium text-ink">
                {item.label || "默认 Key"}
              </span>
              <span className="shrink-0 text-ink-muted">{item.elapsed_ms}ms</span>
              <span
                className={cn(
                  "min-w-0 break-all",
                  item.ok ? "text-ink-muted" : "text-destructive",
                )}
              >
                {item.ok ? item.content : item.error}
              </span>
            </div>
          ))}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Input
          value={newModel}
          onChange={(e) => setNewModel(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          placeholder="手动添加模型名，例如 gpt-4o"
          className="mono"
        />
        <Button variant="primary" size="sm" onClick={add}>
          <Plus className="h-3.5 w-3.5" aria-hidden />
          添加
        </Button>
      </div>

      {draft.models.length > 0 && (
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden />
          <Input
            value={modelSearch}
            onChange={(e) => setModelSearch(e.target.value)}
            placeholder="搜索模型名称…"
            autoComplete="off"
            aria-label="搜索模型名称"
            className="h-8 pl-8 text-sm"
          />
        </div>
      )}

      <ul className="divide-y divide-border rounded-md border border-border">
        {draft.models.length === 0 ? (
          <li className="px-3 py-6 text-center text-xs text-ink-muted">
            还没有模型
          </li>
        ) : (
          <>
            {/* 批量操作工具行：全选 + 清空选择 / 删除所选 / 测试所选 */}
            <li className="flex items-center gap-2 bg-surface-subtle/40 px-3 py-1.5">
              <label className="flex items-center gap-1.5 text-xs text-ink-muted">
                <input
                  type="checkbox"
                  checked={allChecked}
                  ref={(el) => {
                    if (el) el.indeterminate = !allChecked && someChecked;
                  }}
                  onChange={(e) => toggleAllTestModels(e.target.checked)}
                  className="h-3.5 w-3.5 rounded border-border accent-primary"
                  aria-label="全选测试模型"
                />
                全选
              </label>
              <span className="text-xs text-ink-muted">
                已选 {checkedTestModels.size} / {draft.models.length}
              </span>
              <Button
                variant="ghost"
                size="sm"
                className="ml-auto h-7 px-2 text-xs"
                onClick={() => toggleAllTestModels(false)}
                disabled={checkedTestModels.size === 0 || testingAll}
                title="清空选择"
              >
                清空选择
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs text-destructive hover:bg-destructive/10"
                onClick={() => {
                  const toDelete = new Set(checkedTestModels);
                  update(
                    "models",
                    draft.models.filter((m) => !toDelete.has(m.name)),
                  );
                  // 清理已删模型的限额配置，避免孤儿条目残留
                  const limits = { ...draft.model_limits };
                  let limitsChanged = false;
                  for (const name of toDelete) {
                    if (name in limits) {
                      delete limits[name];
                      limitsChanged = true;
                    }
                  }
                  if (limitsChanged) update("model_limits", limits);
                  if (expandedModel && toDelete.has(expandedModel))
                    setExpandedModel(null);
                  toggleAllTestModels(false);
                }}
                disabled={checkedTestModels.size === 0 || testingAll}
                title="删除所选模型"
              >
                <Trash2 className="h-3 w-3" aria-hidden />
                删除所选 ({checkedTestModels.size})
              </Button>
              <Button
                variant="secondary"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={testCheckedModels}
                disabled={checkedTestModels.size === 0 || testingAll}
              >
                <Play className="h-3 w-3" aria-hidden />
                测试所选 ({checkedTestModels.size})
              </Button>
            </li>
            {modelQuery && visibleModels.length === 0 ? (
              <li className="px-3 py-6 text-center text-xs text-ink-muted">
                没有匹配「{modelSearch}」的模型
              </li>
            ) : (
            visibleModels.map(({ m, i }) => {
              const result = modelTestResults[m.name];
              const testing = testingModels.has(m.name);
              const expanded = expandedModel === m.name;
              const limit = draft.model_limits[m.name];
              return (
              <li key={m.id || `new-${i}`} className="px-3">
                <div className="flex items-center gap-3 py-2">
                  <input
                    type="checkbox"
                    checked={checkedTestModels.has(m.name)}
                    onChange={(e) =>
                      toggleTestModelChecked(m.name, e.target.checked)
                    }
                    className="h-3.5 w-3.5 shrink-0 rounded border-border accent-primary"
                    aria-label={`选中 ${m.name}`}
                  />
                  <div className="flex min-w-0 flex-1 items-center gap-2">
                    <span
                      className="mono truncate text-sm text-ink"
                      title={m.source === "auto" ? "自动同步" : "手动添加"}
                    >
                      {m.name}
                    </span>
                    {draft.opencode_compat ? (
                      <UpstreamProtocolLabel protocol={m.upstream_protocol} />
                    ) : null}
                    {limit && (
                      <Pill tone="neutral" className="text-[10px]">
                        限额
                      </Pill>
                    )}
                  </div>

                  {/* 测试状态：进行中 / 成功延迟 / 失败（完整结果在下方展开行） */}
                  <div className="flex min-w-[140px] items-center justify-end gap-1.5">
                    {testing ? (
                      <span className="flex items-center gap-1.5 text-xs text-ink-muted">
                        <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
                        测试中
                      </span>
                    ) : result ? (
                      result.ok ? (
                        <span
                          className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400"
                          title={`延迟 ${result.latency_ms}ms`}
                        >
                          <CheckCircle className="h-3.5 w-3.5" aria-hidden />
                          {result.latency_ms}ms
                        </span>
                      ) : (
                        <span className="flex items-center gap-1.5 text-xs text-red-600 dark:text-red-400">
                          <AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden />
                          失败
                        </span>
                      )
                    ) : null}
                  </div>

                  <div className="flex shrink-0 items-center gap-0.5">
                    <Button
                      variant="ghost"
                      size="icon"
                      className={cn("h-7 w-7", expanded && "bg-surface-subtle")}
                      onClick={() =>
                        setExpandedModel(expanded ? null : m.name)
                      }
                      title="按模型配置"
                      aria-label={`配置 ${m.name}`}
                      aria-expanded={expanded}
                      aria-controls={expanded ? `model-cfg-${m.name}` : undefined}
                    >
                      <ChevronRight
                        className={cn(
                          "h-3.5 w-3.5 transition-transform",
                          expanded && "rotate-90",
                        )}
                        aria-hidden
                      />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => handleTestModel(m.name)}
                      disabled={testing}
                      title="测试该模型"
                      aria-label={`测试 ${m.name}`}
                    >
                      <Play className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => {
                        if (i === 0) return;
                        const next = draft.models.slice();
                        [next[i - 1], next[i]] = [next[i], next[i - 1]];
                        update("models", next);
                      }}
                      disabled={i === 0}
                      aria-label={`上移 ${m.name}`}
                    >
                      <ChevronUp className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => {
                        if (i === draft.models.length - 1) return;
                        const next = draft.models.slice();
                        [next[i], next[i + 1]] = [next[i + 1], next[i]];
                        update("models", next);
                      }}
                      disabled={i === draft.models.length - 1}
                      aria-label={`下移 ${m.name}`}
                    >
                      <ChevronDown className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-destructive hover:bg-destructive/10"
                      onClick={() => {
                        update(
                          "models",
                          draft.models.filter((_, idx) => idx !== i),
                        );
                        // 清理已删模型的限额配置，避免孤儿条目残留
                        if (m.name in draft.model_limits) {
                          const limits = { ...draft.model_limits };
                          delete limits[m.name];
                          update("model_limits", limits);
                        }
                        if (expandedModel === m.name) setExpandedModel(null);
                      }}
                      aria-label={`移除 ${m.name}`}
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </div>
                </div>

                {/* 测试结果展开行：成功显示延迟与回复内容，失败完整显示错误（不截断） */}
                {result && !testing && (
                  <div className="ml-8 border-l-2 border-border/40 pl-3 py-1.5 text-xs">
                    {result.ok ? (
                      <div className="space-y-1">
                        <span className="text-emerald-600 dark:text-emerald-400">
                          ✓ 成功 · 延迟 {result.latency_ms}ms
                        </span>
                        {result.content && (
                          <p className="text-ink-muted whitespace-pre-wrap break-all">{result.content}</p>
                        )}
                      </div>
                    ) : (
                      <div className="space-y-1">
                        <span className="text-red-600 dark:text-red-400">✗ 失败</span>
                        <p className="text-red-600 dark:text-red-400 whitespace-pre-wrap break-all">{result.error}</p>
                      </div>
                    )}
                  </div>
                )}

                {/* 按模型配置：max_output 限额 + thinking_level 注入 */}
                {expanded && (
                  <div
                    id={`model-cfg-${m.name}`}
                    className="grid grid-cols-2 gap-3 rounded-md border border-border bg-surface-subtle/40 p-3"
                  >
                    <Field
                      label="最大输出 token"
                      hint="注入上游请求的 max_output；留空不限制"
                    >
                      <Input
                        type="number"
                        min={0}
                        value={limit?.max_output ?? ""}
                        onChange={(e) => {
                          const v = e.target.value.trim();
                          setModelLimit(
                            m.name,
                            v ? { max_output: Math.max(0, Number(v)) } : { max_output: undefined },
                          );
                        }}
                        placeholder="不限制"
                      />
                    </Field>
                    <Field
                      label="思考等级"
                      hint="注入上游请求的 thinking level；未配置不注入"
                    >
                      <Select
                        className="w-full text-sm"
                        value={limit?.thinking_level ?? ""}
                        onChange={(e) =>
                          setModelLimit(m.name, {
                            thinking_level: e.target.value,
                          })
                        }
                      >
                        <option value="">未配置</option>
                        {THINKING_LEVEL_OPTIONS.map((v) => (
                          <option key={v} value={v}>
                            {v}
                          </option>
                        ))}
                      </Select>
                    </Field>
                  </div>
                )}
              </li>
              );
            })
            )}
          </>
        )}
      </ul>
    </div>
  );
}

// ---------------- 拉取模型选择器 ----------------

function FetchPicker({
  fetched,
  checked,
  existing,
  onToggle,
  onToggleAll,
  onToggleMany,
  onConfirm,
  onCancel,
}: {
  fetched: string[];
  checked: Set<string>;
  existing: Set<string>;
  onToggle: (name: string, checked: boolean) => void;
  onToggleAll: (checked: boolean) => void;
  onToggleMany: (names: string[], checked: boolean) => void;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  // 上游返回的全部模型都列出。已存在于渠道的标灰（disabled）—— 它们已通过
  // `existing` 排除在新增集合之外，但保留展示让用户看到「上游有这个但我已经有了」。
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const filtered = q
    ? fetched.filter((n) => n.toLowerCase().includes(q))
    : fetched;
  // 全选只作用于当前可见（过滤后）的模型。
  const allChecked =
    filtered.length > 0 && filtered.every((n) => checked.has(n) || existing.has(n));
  const someChecked = filtered.some((n) => checked.has(n));
  const newCount = fetched.filter((n) => !existing.has(n)).length;
  const dupCount = fetched.length - newCount;
  const willAdd = fetched.filter((n) => checked.has(n) && !existing.has(n)).length;
  const totalChecked = fetched.filter((n) => checked.has(n)).length;

  function handleToggleAll(checked: boolean) {
    if (q) {
      // 过滤态：只批量勾选可见且非已存在的项。
      onToggleMany(
        filtered.filter((n) => !existing.has(n)),
        checked,
      );
    } else {
      onToggleAll(checked);
    }
  }

  return (
    <div className="space-y-3">
      <div className="rounded-md border border-border bg-card/60 p-3 text-xs">
        <p className="text-ink">
          从上游共发现 <span className="font-medium">{fetched.length}</span> 个模型
          {dupCount > 0 && (
            <>
              {" "}
              · 已存在 <span className="font-medium">{dupCount}</span> 个（自动跳过）
            </>
          )}
          {" "}
          · 可添加 <span className="font-medium">{newCount}</span> 个
        </p>
        <p className="mt-1 text-ink-muted">
          默认全选；取消勾选的不会加入。已存在的模型保留 source 字段不被覆盖。
        </p>
      </div>

      {/* 搜索框 */}
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索模型名称…"
          autoComplete="off"
          className="h-8 pl-8 text-sm"
        />
      </div>

      <div className="flex items-center gap-2 border-b border-border pb-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-muted">
          <input
            type="checkbox"
            checked={allChecked}
            ref={(el) => {
              if (el) el.indeterminate = !allChecked && someChecked;
            }}
            onChange={(e) => handleToggleAll(e.target.checked)}
            className="h-3.5 w-3.5 rounded border-border accent-primary"
          />
          全选{q && filtered.length < fetched.length ? `（${filtered.length}）` : ""}
        </label>
        <span className="text-xs text-ink-muted">
          已勾选 {totalChecked} / {fetched.length}
          {q && filtered.length < fetched.length && (
            <> · 显示 {filtered.length}</>
          )}
        </span>
      </div>

      {filtered.length === 0 ? (
        <p className="rounded-md border border-border bg-surface-subtle/60 px-3 py-6 text-center text-sm text-ink-muted">
          没有匹配「{query}」的模型
        </p>
      ) : (
        <ul className="max-h-[50vh] divide-y divide-border overflow-auto rounded-md border border-border">
          {filtered.map((name) => {
            const isExisting = existing.has(name);
            return (
              <li
                key={name}
                className={cn(
                  "flex items-center gap-2 px-3 py-2 text-sm",
                  isExisting && "opacity-50",
                )}
              >
                <input
                  type="checkbox"
                  disabled={isExisting}
                  checked={isExisting || checked.has(name)}
                  onChange={(e) => onToggle(name, e.target.checked)}
                  className="h-3.5 w-3.5 rounded border-border accent-primary"
                  aria-label={`保留 ${name}`}
                />
                <span className="mono truncate text-ink">{name}</span>
                {isExisting && (
                  <Pill tone="neutral" className="ml-auto">
                    已存在
                  </Pill>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <DialogFooter>
        <Button variant="ghost" size="sm" onClick={onCancel}>
          取消
        </Button>
        <Button
          variant="primary"
          size="sm"
          onClick={onConfirm}
          disabled={willAdd === 0}
        >
          确认添加 {willAdd > 0 && `(${willAdd})`}
        </Button>
      </DialogFooter>
    </div>
  );
}

// ---------------- 限制 Tab ----------------

function LimitsTab({
  draft,
  update,
}: {
  draft: Draft;
  update: <K extends keyof Draft>(k: K, v: Draft[K]) => void;
}) {
  return (
    <div className="space-y-3">
      <Field label="单 Key RPM（0=不限）">
        <Input
          type="number"
          min={0}
          value={draft.rate_limit_rpm}
          onChange={(e) => update("rate_limit_rpm", Number(e.target.value))}
        />
      </Field>
      <Field label="渠道最大并发（0=不限）">
        <Input
          type="number"
          min={0}
          value={draft.max_concurrent}
          onChange={(e) => update("max_concurrent", Number(e.target.value))}
        />
      </Field>
      <Field
        label="模型同步过滤（正则）"
        hint="从上游拉取时只保留匹配项；留空不过滤"
      >
        <Input
          value={draft.match_regex ?? ""}
          onChange={(e) => update("match_regex", e.target.value)}
          placeholder="^gpt-4.*$"
          className="mono"
        />
      </Field>
      <p className="rounded-md bg-surface-subtle/40 p-2.5 text-xs text-ink-muted">
        按模型的 max_output / thinking_level 限额在「模型」Tab
        中点击行首箭头展开配置；模板化批量配置建议导出后手工编辑再导入。
      </p>
    </div>
  );
}

// ---------------- 高级 Tab ----------------

function AdvancedTab({
  draft,
  update,
}: {
  draft: Draft;
  update: <K extends keyof Draft>(k: K, v: Draft[K]) => void;
}) {
  // 请求头模板来自设置页维护的 header_templates 设置项，渠道表单一键填充。
  const { data: settings } = useQuery({
    queryKey: ["settings", "list"],
    queryFn: api.listSettings,
  });
  const headerTemplates = useMemo(
    () =>
      parseHeaderTemplates(
        settings?.find((s) => s.key === HEADER_TEMPLATES_SETTING_KEY)?.value,
      ),
    [settings],
  );
  // 代理池来自设置页维护的 proxy_pool 设置项，渠道表单可下拉选择。
  const proxyPool = useMemo<ProxyEntry[]>(() => {
    const raw = settings?.find((s) => s.key === "proxy_pool")?.value;
    if (!raw) return [];
    try {
      const arr = JSON.parse(raw);
      return Array.isArray(arr) ? arr.filter((p: ProxyEntry) => p.enabled && p.url) : [];
    } catch {
      return [];
    }
  }, [settings]);
  const [selectedTemplate, setSelectedTemplate] = useState("");

  /**
   * 一键按模板填充：同名 Key（不区分大小写）用模板值覆盖，新 Key 追加；
   * 同时清掉表单里未填 Key 的占位行。
   */
  function applyHeaderTemplate(name: string) {
    const tpl = headerTemplates.find((t) => t.name === name);
    if (!tpl) return;
    const byKey = new Map<string, Draft["custom_header"][number]>();
    for (const h of draft.custom_header ?? []) {
      const key = h.header_key.trim();
      if (key) byKey.set(key.toLowerCase(), { header_key: key, header_value: h.header_value });
    }
    for (const h of tpl.headers) {
      const key = h.header_key.trim();
      if (!key) continue;
      byKey.set(key.toLowerCase(), { header_key: key, header_value: h.header_value });
    }
    const next = Array.from(byKey.values());
    update("custom_header", next);
    toast.success(`已应用模板「${tpl.name}」：${next.length} 个 Header`);
  }

  return (
    <div className="space-y-4">
      <Field
        label="渠道代理（可选）"
        hint="可从代理池下拉选择，也可手动填写；支持 http(s) 与 socks5/socks5h。管理员界面直接显示完整代理。需开启下方「启用代理」开关才会生效"
      >
        {proxyPool.length > 0 && (
          <Select
            className="mb-1.5 h-7 w-full text-xs"
            value=""
            onChange={(e) => {
              if (!e.target.value) return;
              update("channel_proxy", e.target.value);
            }}
            aria-label="从代理池选择"
          >
            <option value="">从代理池选择…</option>
            {proxyPool.map((p) => (
              <option key={p.id} value={p.url}>
                {p.name}
              </option>
            ))}
          </Select>
        )}
        <Input
          value={draft.channel_proxy ?? ""}
          onChange={(e) => update("channel_proxy", e.target.value)}
          placeholder="http://127.0.0.1:7890"
          className="mono"
          aria-label="渠道代理"
        />
      </Field>
      <Field label="参数覆盖（JSON）">
        <Textarea
          rows={5}
          className="mono"
          value={draft.param_override ?? ""}
          onChange={(e) => update("param_override", e.target.value)}
          placeholder='{"temperature": 0.7}'
        />
      </Field>

      <div>
        <div className="mb-2 flex items-center justify-between">
          <span className="text-xs font-medium text-ink-muted">自定义 Header</span>
          <div className="flex items-center gap-1.5">
            {headerTemplates.length > 0 && (
              <>
                <Select
                  className="h-7 pl-1.5 pr-6 text-xs"
                  value={selectedTemplate}
                  onChange={(e) => setSelectedTemplate(e.target.value)}
                  aria-label="选择 Header 模板"
                >
                  <option value="">模板</option>
                  {headerTemplates.map((t) => (
                    <option key={t.name} value={t.name}>
                      {t.name}（{t.headers.length} 项）
                    </option>
                  ))}
                </Select>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={() => applyHeaderTemplate(selectedTemplate)}
                  disabled={!selectedTemplate}
                  title="按模板一键填充（同名覆盖，其余追加）"
                >
                  <WandSparkles className="h-3 w-3" aria-hidden />
                  一键填充
                </Button>
              </>
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                update("custom_header", [
                  ...draft.custom_header,
                  { header_key: "", header_value: "" },
                ])
              }
            >
              <Plus className="h-3.5 w-3.5" aria-hidden />
              添加
            </Button>
          </div>
        </div>
        <p className="mb-2 text-xs text-ink-muted/70">
          值中可用 <code className="rounded bg-muted px-1">{'{client_header:xxx}'}</code> 占位符引用客户端请求头，如 <code className="rounded bg-muted px-1">{'{client_header:User-Agent}'}</code>
        </p>
        <div className="space-y-2">
          {draft.custom_header.map((h, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={h.header_key}
                onChange={(e) => {
                  const next = [...draft.custom_header];
                  next[i] = { ...next[i], header_key: e.target.value };
                  update("custom_header", next);
                }}
                placeholder="Header 名"
                className="flex-1"
              />
              <Input
                value={h.header_value}
                onChange={(e) => {
                  const next = [...draft.custom_header];
                  next[i] = { ...next[i], header_value: e.target.value };
                  update("custom_header", next);
                }}
                placeholder="值"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon"
                onClick={() =>
                  update(
                    "custom_header",
                    draft.custom_header.filter((_, idx) => idx !== i),
                  )
                }
                aria-label="移除"
              >
                <X className="h-3.5 w-3.5" aria-hidden />
              </Button>
            </div>
          ))}
        </div>
      </div>

      <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
        <div>
          <p className="text-sm font-medium text-ink">自动同步模型</p>
          <p className="text-xs text-ink-muted">
            开启后按 sync 间隔自动从上游拉取新模型
          </p>
        </div>
        <Switch
          checked={draft.auto_sync}
          onCheckedChange={(v) => update("auto_sync", v)}
        />
      </div>

      <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
        <div>
          <p className="text-sm font-medium text-ink">启用代理</p>
          <p className="text-xs text-ink-muted">
            开启后出站走代理：优先使用上方渠道代理，留空时走设置页的全局代理；关闭则一律直连
          </p>
        </div>
        <Switch
          checked={draft.proxy}
          onCheckedChange={(v) => update("proxy", v)}
        />
      </div>
      <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
        <div>
          <p className="text-sm font-medium text-ink">完全渠道透传</p>
          <p className="text-xs text-ink-muted">
            启用后任意客户端协议均原样透传至上游，不经协议转换；上游需自行兼容客户端协议格式
          </p>
        </div>
        <Switch
          checked={draft.pass_through_body_enabled}
          onCheckedChange={(v) => update("pass_through_body_enabled", v)}
        />
      </div>
    </div>
  );
}

