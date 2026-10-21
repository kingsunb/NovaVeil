/** 单次探测允许的最大标称规模，与后端 contextProbeMaxTokens 一致（十进制 1M）。 */
export const MAX_PROBE_TOKENS = 1_000_000;

/**
 * 固定档位为标称值: 后端构造请求时会自动留冗余(100 万级按 2%, 其余 1K),
 * 「应用」写回的窗口也是这里的标称值(如 1M 档写 1,000,000), 不带冗余。
 * 单位统一十进制: 1K = 1,000, 1M = 1,000,000。
 */
export const PROBE_SIZES = [
  { label: "64K", value: 64_000 },
  { label: "128K", value: 128_000 },
  { label: "256K", value: 256_000 },
  { label: "500K", value: 500_000 },
  { label: "1M", value: 1_000_000 },
] as const;
