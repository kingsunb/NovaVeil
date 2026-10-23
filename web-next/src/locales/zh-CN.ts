export const zhCN = {
  channelTest: {
    emptyContent: "请求成功，但未返回文本内容；可在日志中查看响应。",
    failed: "测试失败",
    keyFailed: "逐密钥测试失败",
  },
  channelEditor: {
    manualContextWindowTitle: "手动上下文窗口：参与路由过滤",
    syncedContextWindow: (tokens: string) => `同步 ${tokens}（仅展示）`,
    syncedContextWindowTitle: "同步目录窗口：仅展示，不参与路由过滤",
    contextWindowHint: "留空表示不参与路由过滤；同步目录值仅展示",
    contextWindowPlaceholder: "未填",
  },
  contextProbe: {
    emptyWindow: "请输入窗口 token 数；清除请使用清除按钮",
    invalidWindow: "窗口必须是大于 0 的整数 token 数",
    saveFailed: "保存窗口失败",
    clearFailed: "清除窗口失败",
    windowSaved: "已保存窗口定义",
    windowCleared: "已清除人工窗口",
    saveDisabledHint: "输入为空；如需清除已定义窗口请点「清除」",
    definedWindow: "已定义",
    syncedWindow: "同步（仅展示）",
    definedWindowTitle: (exact: string) =>
      `手动上下文窗口：${exact}；参与路由过滤`,
    syncedWindowTitle: (exact: string) =>
      `同步目录值：${exact}；仅展示，不参与路由过滤`,
    windowRoutingHint:
      "探测或手动保存写入的是手动窗口，会成为路由限制；同步目录值仅展示。",
    concurrencyHint: (models: number, sizes: number) =>
      `已选 ${models} 个模型 × ${sizes} 个规模；全局三路并发，同一上游端点的同一模型按档位从小到大串行`,
    batchSelectFirst: "先勾选模型",
    batchWindowInvalid: "批量窗口必须是大于 0 的整数 token 数",
    batchPartialFailure: (failed: number, reasons: string) =>
      `${failed} 个模型保存失败：${reasons}`,
    batchClearPartialFailure: (failed: number, reasons: string) =>
      `${failed} 个模型清除失败：${reasons}`,
    batchDefined: (count: number) => `已为 ${count} 个模型定义窗口`,
    batchCleared: (count: number, skipped: number) =>
      skipped > 0
        ? `已清除 ${count} 个模型的人工窗口，跳过 ${skipped} 个未填手动窗口的模型`
        : `已清除 ${count} 个模型的人工窗口`,
    batchClearNone: "所选模型均未定义人工窗口，无需清除",
    appliedWindow: (model: string, tokens: string) =>
      `已把 ${model} 的上下文窗口写为 ${tokens}`,
    applyFailed: "写入失败",
  },
  proxyAccounts: {
    title: "OpenCode 账号体检",
    description:
      "对免费档渠道的代理账号池并发体检：每个账号发一次极小真实请求。健康账号自动入池供转发优先复用；403/429 自动冷却 24 小时。",
    eligibleEmpty: "没有可体检的渠道：需要启用中的 OpenCode 免费档渠道，且渠道代理包含 {account} 占位符",
    channel: "体检渠道",
    channelHint: "仅列出启用中且代理含 {account} 占位符的免费档渠道",
    count: "生成数量",
    concurrency: "并发数",
    concurrencyHint: "1-20；同一时刻在途的体检请求数",
    customAccounts: "自定义账号列表（可选）",
    customAccountsPlaceholder: "每行一个账号；留空则按「生成数量」随机生成",
    run: "开始体检",
    running: "体检中…",
    runDone: (healthy: number, rejected: number, failed: number) =>
      `体检完成：健康 ${healthy}，被拒 ${rejected}，失败 ${failed}`,
    countInvalid: "生成数量须为 1-200 的整数",
    concurrencyInvalid: "并发数须为 1-20 的整数",
    outcomeHealthy: "健康",
    outcomeRejected: "被拒",
    outcomeFailed: "失败",
    columnAccount: "账号",
    columnOutcome: "结论",
    columnStatus: "HTTP",
    columnLatency: "延迟",
    columnDetail: "详情",
    summaryTotal: "合计",
    summaryHealthy: "健康",
    summaryRejected: "被拒",
    summaryFailed: "失败",
    latencyMS: (ms: number) => `${ms} ms`,
    detailHint: "上游响应摘要；完整请求见日志面板",
    modelLabel: (model: string) => `探测模型：${model}`,
    requestFailed: "体检请求失败",
  },
  groups: {
    manualWindowUnset: "未填（不参与过滤）",
    manualWindowUnsetTitle:
      "手动窗口未填：不参与过滤，请求会照常尝试；同步目录值仅供参考",
    syncedWindow: (exact: string) => `同步 ${exact}（仅展示）`,
    syncedWindowTitle: (exact: string) =>
      `同步目录窗口：${exact}；仅展示，不参与路由过滤`,
    definedWindowTitle: (exact: string) =>
      `手动上下文窗口：${exact}；请求估算超出即跳过该成员`,
    windowSuffix: "窗口",
  },
};
