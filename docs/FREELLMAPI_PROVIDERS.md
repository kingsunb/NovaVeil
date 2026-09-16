# FreeLLMAPI 免费供应商清单

> 来源项目：[tashfeenahmed/freellmapi](https://github.com/tashfeenahmed/freellmapi)
>
> 本文档整理自 `server/src/providers/index.ts` 中的 `register()` 注册调用与 `shared/types.ts` 的 `Platform` 类型定义，记录该项目聚合的全部免费 LLM 供应商信息，供 NovaVeil 渠道配置与供应商对接参考。

## 概述

FreeLLMAPI 将数十个免费 LLM 供应商的免费额度聚合到一个 OpenAI 兼容的 `/v1` API 后面。路由器自动挑选最佳可用模型，被限流时自动切换到下一个供应商，并追踪每个 key 的用量以保持在免费额度内。

- README 宣传数字：**34 个免费供应商**（已过时）
- 代码实际注册：**48 个真实供应商** + 1 个 `custom` 占位符
- 已移除供应商：`sambanova`（V23 移除，免费层永久取消）、`chutes`（评估后放弃）、Moonshot/MiniMax 直连（V4 移除）

---

## 一、大型云厂商 / 第一方 API（12 个）

| # | 平台 ID | 名称 | Base URL | 适配器 | 备注 |
|---|---------|------|----------|--------|------|
| 1 | `google` | Google (Gemini) | — | `GoogleProvider` | Gemini 原生格式，60s 超时（Gemma 推理冷启动 20-60s） |
| 2 | `groq` | Groq | `api.groq.com/openai/v1` | `OpenAICompat` | 超快推理 |
| 3 | `cerebras` | Cerebras | `api.cerebras.ai/v1` | `OpenAICompat` | Wafer-scale 推理 |
| 4 | `nvidia` | NVIDIA NIM | `integrate.api.nvidia.com/v1` | `OpenAICompat` | 单工具调用，180s 超时（推理冷启动 30-60s） |
| 5 | `mistral` | Mistral | `api.mistral.ai/v1` | `OpenAICompat` | |
| 6 | `cohere` | Cohere | — | `CohereProvider` | 兼容端点 |
| 7 | `cloudflare` | Cloudflare Workers AI | — | `CloudflareProvider` | key = `account_id:token` |
| 8 | `zhipu` | Z.ai (智谱) | — | `ZhipuProvider` | 双控制台自动探测（open.bigmodel.cn / api.z.ai），60s 超时 |
| 9 | `github` | GitHub Models | `models.github.ai/inference` | `OpenAICompat` | 使用 `<publisher>/<model>` ID |
| 10 | `huggingface` | HuggingFace Router | `router.huggingface.co/v1` | `OpenAICompat` | $0.10/月 路由额度，无需信用卡 |
| 11 | `ollama` | Ollama Cloud | `ollama.com/v1` | `OpenAICompat` | 120s 超时，部分模型需订阅 |
| 12 | `sail` | Sail Research | — | `SailProvider` | 后台轮询模式，$5/月免费额度（需绑定支付方式） |

---

## 二、聚合网关 / 路由器（19 个）

| # | 平台 ID | 名称 | Base URL | 适配器 | 备注 |
|---|---------|------|----------|--------|------|
| 13 | `openrouter` | OpenRouter | `openrouter.ai/api/v1` | `OpenAICompat` | 带额外 header（HTTP-Referer / X-Title） |
| 14 | `electronhub` | ElectronHub | — | `ElectronHubProvider` | 每周额度续期 |
| 15 | `experiential` | Experiential | — | `ExperientialProvider` | 每月额度续期 |
| 16 | `router9` | Router9 | — | `Router9Provider` | 共享月额度 |
| 17 | `septor` | Septor | — | `SeptorProvider` | 零价模型共享日配额 |
| 18 | `clod` | Clod | — | `ClodProvider` | |
| 19 | `speechify` | Speechify | — | `SpeechifyProvider` | |
| 20 | `blaze` | Blaze | — | `BlazeProvider` | |
| 21 | `bai` | B.AI | `api.b.ai/v1` | `OpenAICompat` | 限时 0 额度推广 |
| 22 | `anyapi` | AnyAPI | `api.anyapi.ai/v1` | `OpenAICompat` | 100K token/天，仅 free/basic 模型 |
| 23 | `kilo` | Kilo Gateway | `api.kilo.ai/api/gateway/v1` | `OpenAICompat` | **免 key**，200 req/hr/IP |
| 24 | `llm7` | LLM7 | `api.llm7.io/v1` | `OpenAICompat` | 100 req/hr，匿名可用 |
| 25 | `opencode` | OpenCode Zen | `opencode.ai/zen/v1` | `OpenAICompat` | 限时免费模型，无需信用卡 |
| 26 | `ovh` | OVH AI Endpoints | `oai.endpoints.kepler.ai.cloud.ovh.net/v1` | `OpenAICompat` | **免 key**，2 req/min/IP |
| 27 | `routeway` | Routeway | `api.routeway.ai/v1` | `OpenAICompat` | 需浏览器 UA（CF 拦截），~5 rpm |
| 28 | `bazaarlink` | BazaarLink | `bazaarlink.ai/api/v1` | `OpenAICompat` | `auto:free` 路由自动选零价模型 |
| 29 | `ainative` | AINative Studio | `api.ainative.studio/api/v1` | `OpenAICompat` | ~10M token/月（未验证） |
| 30 | `aion` | Aion Labs | `api.aionlabs.ai/v1` | `OpenAICompat` | 无需信用卡 |
| 31 | `requesty` | Requesty | `router.requesty.ai/v1` | `OpenAICompat` | 无需信用卡 |

---

## 三、亚太地区（5 个）

| # | 平台 ID | 名称 | Base URL | 适配器 | 备注 |
|---|---------|------|----------|--------|------|
| 32 | `radeon` | AMD Radeon Cloud | `developer.amd.com.cn/radeon/api/v1` | `OpenAICompat` | 单工具调用，600s 超时 |
| 33 | `navy` | NavyAI | `api.navy/v1` | `OpenAICompat` | 150K token/天，20 RPM，需显式 UA |
| 34 | `nara` | NaraRouter | `router.bynara.id/v1` | `OpenAICompat` | 需 Telegram 频道/链接验证 |
| 35 | `sealion` | SEA-LION (AI Singapore) | `api.sea-lion.ai/v1` | `OpenAICompat` | Google 登录，10 RPM，无区域墙 |
| 36 | `xkiro` | xKiro | `api.xkiro.com/v1` | `OpenAICompat` | 5M token/天，付费模型 403 |

---

## 四、中国国内厂商（5 个）

> 均需中国实名认证（LongCat 除外——支持中国大陆以外邮箱注册）。

| # | 平台 ID | 名称 | Base URL | 适配器 | 备注 |
|---|---------|------|----------|--------|------|
| 37 | `modelscope` | ModelScope (魔搭/阿里) | `api-inference.modelscope.cn/v1` | `ModelScopeProvider` | 2000 req/天，需绑定阿里云中国站 |
| 38 | `qianfan` | 百度千帆 | `qianfan.baidubce.com/v2` | `OpenAICompat` | ERNIE-Speed/Lite/Tiny 免费，按速率限制 |
| 39 | `volcengine` | 火山方舟 (字节) | `ark.cn-beijing.volces.com/api/v3` | `OpenAICompat` | 2M token/天/模型（最强循环免费额度） |
| 40 | `longcat` | LongCat (美团) | `api.longcat.chat/openai/v1` | `OpenAICompat` | 日免费额度，另有 Anthropic 兼容端点 |
| 41 | `xfyun` | 讯飞星火 | `spark-api-open.xf-yun.com/v1` | `OpenAICompat` | Lite 模型免费，Bearer = APIPassword |

---

## 五、社区 / 小众（7 个）

| # | 平台 ID | 名称 | Base URL | 适配器 | 备注 |
|---|---------|------|----------|--------|------|
| 42 | `pollinations` | Pollinations | — | `PollinationsProvider` | 需 publishable key，验证探针 /account/key |
| 43 | `agnes` | Agnes AI (Sapiens) | `apihub.agnes-ai.com/v1` | `OpenAICompat` | $0/token 推广期，60s 超时 |
| 44 | `reka` | Reka | `api.reka.ai/v1` | `OpenAICompat` | 月额度，原生多模态（图/视频） |
| 45 | `siliconflow` | SiliconFlow | `api.siliconflow.com/v1` | `OpenAICompat` | 免费 image（FLUX.1）/TTS（CosyVoice2）模型 |
| 46 | `orcarouter` | OrcaRouter | `api.orcarouter.ai/v1` | `OpenAICompat` | `*-free` 别名，429 不回退付费 |
| 47 | `unorouter` | UnoRouter | `api.unorouter.com/v1` | `OpenAICompat` | `:free` 后缀，按分钟限速 |
| 48 | `aihorde` | AI Horde | — | `AIHordeProvider` | 社区志愿算力，**免 key**（匿名 `0000000000`），120s 超时 |

---

## 六、自定义端点

| 平台 ID | 名称 | 说明 |
|---------|------|------|
| `custom` | Custom (OpenAI-compatible) | 用户可指向任意 OpenAI 兼容端点（llama.cpp、LM Studio、vLLM、本地 Ollama、远程网关等），Base URL 存储在 `api_keys` 行上，120s 超时 |

---

## 统计摘要

| 类别 | 数量 |
|------|------|
| 大型云厂商 / 第一方 API | 12 |
| 聚合网关 / 路由器 | 19 |
| 亚太地区 | 5 |
| 中国国内厂商 | 5 |
| 社区 / 小众 | 7 |
| **总计（真实供应商）** | **48** |
| 自定义端点（占位符） | 1 |

---

## 已移除 / 放弃的供应商

| 供应商 | 移除版本 | 原因 |
|--------|----------|------|
| SambaNova | V23 (2026-06) | 免费层永久取消，改为一次性 $5 试用额度（3 个月过期） |
| Chutes | V11 评估 | 所有模型返回 402，"免费"层需非零余额，与无信用卡标准冲突 |
| Moonshot 直连 | V4 | 仅付费 |
| MiniMax 直连 | V4 | 被 OpenRouter 路由取代 |

---

## 适配器类型说明

| 适配器类 | 说明 |
|----------|------|
| `OpenAICompatProvider` | 通用 OpenAI 兼容适配器，绝大多数供应商使用 |
| `GoogleProvider` | Gemini 原生 API 格式转换 |
| `CohereProvider` | Cohere 兼容端点 |
| `CloudflareProvider` | key 格式 `account_id:token` |
| `ZhipuProvider` | 双控制台（国内/国际）自动探测 |
| `ModelScopeProvider` | GET /v1/models 对垃圾 token 也返回 200，需 chat 探针验证 |
| `PollinationsProvider` | GET /v1/models 公开（200 即使 key 已吊销），验证探针 /account/key |
| `AIHordeProvider` | 队列式，max_tokens ≥ 16，无工具调用，usage 以 kudos 计 |
| `SailProvider` | Responses API + 后台轮询 |
| `ElectronHubProvider` / `ExperientialProvider` / `Router9Provider` / `SeptorProvider` / `ClodProvider` / `SpeechifyProvider` / `BlazeProvider` | 各自独立适配器，处理额度/配额差异 |

---

## 免 key 供应商

以下 3 个供应商支持匿名（无 API key）访问，注册时标记 `keyless: true`，路由器自动配置：

| 平台 ID | 名称 | 限制 |
|---------|------|------|
| `kilo` | Kilo Gateway | 200 req/hr/IP |
| `ovh` | OVH AI Endpoints | 2 req/min/IP/model |
| `aihorde` | AI Horde | 匿名 key `0000000000`，最低队列优先级 |

---

## 参考文件

| 文件 | 说明 |
|------|------|
| `server/src/providers/index.ts` | 全部供应商注册入口 |
| `server/src/providers/base.ts` | 供应商基类定义 |
| `server/src/providers/openai-compat.ts` | 通用 OpenAI 兼容适配器 |
| `shared/types.ts` | `Platform` 联合类型定义 |
