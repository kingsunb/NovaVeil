/**
 * 模型评估页核心逻辑：固定测试题、包裹标记、HTML 提取与格式判定。
 *
 * 评估流程：对每个渠道的每个模型发送 EVAL_PROMPT，模型须把完整 HTML 放在
 * RESULT_START / RESULT_END 之间。排序资格看请求是否成功，不看格式是否合规：
 *  - 测试报错（error）→ 只留历史与失败原因，不进入排序，也不覆盖已有成功快照
 *  - 请求成功（ok 与 violation）→ 进入排序，并可写入 auto 分组
 * 管理台「加入排序」按钮目前只对 ok 可点，比 from-history 接口更窄。
 */

/** 包裹起始标记。 */
export const RESULT_START = "<<<RESULT>>>";
/** 包裹结束标记。 */
export const RESULT_END = "<<<END>>>";

/**
 * 固定测试题。要求模型生成一个 SVG 鹈鹕骑自行车 2D 动画的 HTML，
 * 并把完整代码放在 <<<RESULT>>> / <<<END>>> 之间，标记外不输出任何内容。
 * 与 internal/model/model_eval.go 中的 ModelEvalPrompt 保持一致。
 * 实际评估使用后端固定题目；历史详情展示当次保存的题目快照。
 */
export const EVAL_PROMPT =
  "创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画，不要联网，直接做出来。" +
  `请将最终完整的 HTML 代码放在 ${RESULT_START} 和 ${RESULT_END} 之间，` +
  "这两个标记之外不要输出任何其他内容。";

/** 评估排序自动写入的分组名称。 */
export const AUTO_GROUP_NAME = "auto";

/** 并发测试的 worker 数，与渠道编辑器批量测试保持一致，避免触发上游风控。 */
export const EVAL_CONCURRENCY = 4;

/**
 * extractEvalHtml 从模型回复中提取包裹标记之间的 HTML。
 *
 * @returns 找到包裹时 { wrapped: true, html }；未找到时 { wrapped: false, html: "" }。
 *          多次出现包裹时取第一组；标记间内容做 trim，避免首尾空白撑高 iframe。
 */
export function extractEvalHtml(content: string): {
  wrapped: boolean;
  html: string;
} {
  if (!content) return { wrapped: false, html: "" };
  const startIdx = content.indexOf(RESULT_START);
  if (startIdx === -1) return { wrapped: false, html: "" };
  const afterStart = startIdx + RESULT_START.length;
  const endIdx = content.indexOf(RESULT_END, afterStart);
  if (endIdx === -1) return { wrapped: false, html: "" };
  const html = content.slice(afterStart, endIdx).trim();
  if (html.length === 0) return { wrapped: false, html: "" };
  return { wrapped: true, html };
}

/**
 * extractRenderableHtml 从模型回复中尽力提取可渲染的 HTML，用于人工评估预览。
 *
 * 依次尝试：
 *  1. <<<RESULT>>> … <<<END>>>  包裹标记（首选）
 *  2. ```html … ``` 或 ``` … ```  markdown 代码围栏
 *  3. 裸 HTML 检测（含 <html / <svg / <!DOCTYPE / <body）
 *
 * 即使模型没按格式包裹，也尽量把产出渲染出来让用户目视评估。
 */
export function extractRenderableHtml(content: string): string {
  if (!content) return "";
  // 1. 包裹标记
  const { wrapped, html } = extractEvalHtml(content);
  if (wrapped) return html;
  // 2. markdown 代码围栏 ```html … ``` 或 ``` … ```
  const fence = content.match(/```(?:html)?\s*\n?([\s\S]*?)```/i);
  if (fence?.[1]) {
    const inner = fence[1].trim();
    if (/<(?:html|svg|body|div|!doctype)/i.test(inner)) return inner;
  }
  // 3. 裸 HTML
  if (/<(?:html|svg|body|!doctype)/i.test(content)) {
    // 取第一个 < 到末尾，尽量不把解释性文字混进 iframe
    const firstTag = content.search(/<(?:html|svg|body|!doctype)/i);
    if (firstTag >= 0) return content.slice(firstTag).trim();
  }
  return "";
}

/**
 * EvalOutcome 单个渠道模型的评估终态。
 *  - error      : 测试请求失败（上游报错/超时/拒绝），不进入排序，也不覆盖已有成功快照。
 *  - violation  : 请求成功但未按格式包裹，与 ok 一样可以进入排序并写入 auto 分组。
 *  - ok         : 成功且包裹合规，可以进入排序。
 */
export type EvalOutcome = "ok" | "violation" | "error";

/** 评估候选：一个渠道上的一个模型。 */
export interface EvalTarget {
  channelId: number;
  channelName: string;
  channelType: string;
  /** ChannelModel.id，创建分组成员时需要。 */
  channelModelId: number;
  modelName: string;
}

/** 评估结果条目。 */
export interface EvalResult {
  target: EvalTarget;
  outcome: EvalOutcome;
  /** 模型原始回复全文。 */
  content: string;
  /** 提取出的可渲染 HTML（ok 和 violation 都可能有值，error 时为空）。 */
  html: string;
  latencyMs: number;
  promptTokens: number;
  completionTokens: number;
  /** outcome=error 时的失败原因。 */
  error: string;
}

/** 与后端模型一致的持久化摘要；列表不携带原始回复。 */
export interface EvalRecordSummary {
  id: number;
  channel_id: number;
  channel_model_id: number;
  channel_name: string;
  channel_type: string;
  model_name: string;
  outcome: EvalOutcome;
  created_at: string;
  completed_at: string;
  latency_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  error: string;
  content_truncated: boolean;
}

export interface EvalRecord extends EvalRecordSummary {
  prompt: string;
  content: string;
}

export interface EvalHistoryQuery {
  channel_id?: number;
  model_name?: string;
  outcome?: EvalOutcome;
  query?: string;
  page?: number;
  page_size?: number;
}

export interface EvalHistoryPage {
  items: EvalRecordSummary[];
  total: number;
  page: number;
  page_size: number;
}

/** 按渠道和模型独立累计，不受历史裁剪和清理影响；成功指格式合规。 */
export interface EvalStatsSummary {
  channel_id: number;
  model_name: string;
  total_count: number;
  success_count: number;
}

export interface EvalStatsList {
  items: EvalStatsSummary[];
}

export function formatEvalTime(value: string): string {
  return new Date(value).toLocaleString("zh-CN", { hour12: false });
}

/** 同名模型在不同渠道独立记录；同一渠道重测只替换当前排序中的结果。 */
export function sameEvalTarget(a: EvalRecordSummary, b: EvalRecordSummary): boolean {
  return a.channel_id === b.channel_id && a.model_name === b.model_name;
}

/** 历史快照不能直接用来创建分组，先解析当前仍存在且启用的渠道模型。 */
export function findEvalTarget(record: EvalRecordSummary, targets: EvalTarget[]): EvalTarget | undefined {
  return targets.find((target) => target.channelId === record.channel_id && target.modelName === record.model_name);
}

/**
 * 归一化优先级：把用户拖拽顺序（索引 0 = 最优先）映射为后端 priority 值。
 * 后端 priority 越大越靠前，所以用 (length - index) 让索引 0 拿到最高值。
 */
export function priorityFromOrder(index: number, total: number): number {
  return total - index;
}

/** 评估队列任务状态。 */
export type QueueStatus = "queued" | "running" | "done" | "stopped";

/** 评估排序条目摘要（不含 content）。 */
export interface EvalRankSummary {
  id: number;
  channel_id: number;
  channel_model_id: number;
  channel_name: string;
  channel_type: string;
  model_name: string;
  outcome: EvalOutcome;
  error: string;
  content_truncated: boolean;
  prompt_tokens: number;
  completion_tokens: number;
  latency_ms: number;
  source_eval_id: number;
  position: number;
  created_at: string;
  updated_at: string;
}

/** 评估排序条目内容。 */
export interface EvalRankContent {
  content: string;
  content_truncated: boolean;
}

/** 评估队列任务。 */
export interface EvalQueueTask {
  id: number;
  channel_id: number;
  channel_model_id: number;
  channel_name: string;
  channel_type: string;
  model_name: string;
  status: QueueStatus;
  error: string;
  eval_id: number;
  position: number;
  created_at: string;
  started_at: string;
  completed_at: string;
}

export interface EvalRankList {
  items: EvalRankSummary[];
}

export interface EvalQueueList {
  items: EvalQueueTask[];
}

export interface EvalEnqueueResult {
  enqueued: EvalQueueTask[];
}

export interface EvalApplyProResult {
  group_id: number;
  created: boolean;
  item_count: number;
}

export interface EvalRemovedResult {
  removed: number;
}
