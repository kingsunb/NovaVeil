# Agent Note: opencode 免费档系统提示门禁与派生会话号时间段修复

Status: implemented

## Problem

TLS 指纹(sigalgs)修复后, v1 档(/zen)出现「账号体检 200 健康、真实转发立刻 429」的劈叉; v2 档(/inference)则连官方逐字节体检模板也全 429。远程受控实验(同额度出口、单变量隔离)定位出两道新门禁与一层额度动态:

1. **系统提示门禁(v1 档)**: 上游要求请求带完整的官方系统提示。官方模板去掉 system 立即 429(工具集/max_tokens/消息内容均无关, 单变量排除); 普通客户端 system("You are a helpful assistant.")与官方模板截断 200 字符同样 429; 官方模板尾部追加任意客户端指令则 200。体检带官方模板所以通过, 下游真实请求(无官方 system)被拒。
2. **会话号时间段校验(两档)**: `SessionIDFromSignal` 用哈希直接构造 ses_ 号时间段, 解码落在 1970 年; 官方号时间段解码是当前时间。同一出口同一形态下, 随机新号(当前时间)200、哈希派生号(1970)429(99ms 快拒)。真实转发对带会话信号的客户端走派生路径, 恒被拒。
3. **免费额度按出口 IP 且动态竞争**: 同一 IP 真客户端连续两次调用第二次即 429, 换新 IP 立即 200——住宅代理池 IP 全球共享, 余额随时被其他用户消耗, 新号突击模式额度约等于一次请求。体检本身每次 200 都在消耗该 IP 余额, 「体检健康 → 转发时余额已被烧掉/抢空」是劈叉的第三重机制。oc-bridge 的三个粘性 IP 长期可用, 说明稳定粘性使用模式与突击新号在额度待遇上不同。
4. **v2 档无差别 429**: 在有额度的出口上, v2 档连官方逐字节模板也 429, 与形态无关; 该出口池对 /inference 整体关闭或按更粗 IP 段限额, 代码层无法对齐, 只能换出口或等政策变化。

## Decision

1. **官方系统提示注入(`internal/relay/opencode_free.go`)**: `ensureOfficialOpencodeSystem` 在免费档 chat 整形链中保证首条 system 是官方模板——无 system 时插入, 有客户端 system 时以官方模板打头、客户端原文以空行追加到尾部(实测追加不影响门禁判定); 模板复用体检的 `loadOcProbeTemplates`(按 v1/v2 档位, MODEL/DATE 动态化)。首条 system 已是官方形态(前 64 字节前缀命中)时幂等跳过, 体检模板与重复整形不受影响。v1/v2 分支均在键序归一前调用。
2. **派生会话号时间段修复(`internal/utils/opencodeid/opencodeid.go`)**: `SessionIDFromSignal` 的时间段从哈希构造改为当前时间的小时锚(`UnixMilli` 截断到小时再左移 12 位), 计数低位 12 bit 与 14 位随机段仍取自哈希——同一信号在同一小时内派生同一号(稳定性覆盖会话粘合 30 分钟 TTL), 跨小时等效新会话, 解码时间始终落在近期。
3. **TLS 指纹锁定测试(`internal/client/bun_hello_test.go`)**: uTLS 本地 marshal 出 ClientHello 原始字节, 断言扩展顺序、签名算法完整序列(含 Ed25519/P-521)、密码套件、曲线首组 X25519MLKEM768、ALPN 与版本——TLS 回归不再静默通过 go test。

## Alternatives considered

- **转发时逐字节替换客户端 system 为官方模板** — 最强伪装; 否掉: 吞掉客户端指令会改变模型行为, 官方尾部追加已被实测放行, 无需牺牲。
- **派生号全量改随机 + 调用方缓存** — 时间段天然合法; 否掉: `handler` 主路径无缓存, 全量随机会破坏同会话粘合, 小时锚以最小改动同时满足合法与稳定。
- **v2 档同步排查形态差异** — 有额度出口上官方模板已 429, 无形态可修; 转为记录现状与换号对策。

## Consequences

- **收益**: v1 档下游真实转发(含带会话信号客户端)与体检同待遇; 会话号不再解码到 1970 年; TLS 指纹回归有测试兜底。
- **代价与已知上限**: 免费档请求正文恒带约 10KB 官方系统提示, token 消耗上升(官方客户端同款形态); 客户端指令位于大 system 尾部, 模型遵循度可能弱于原生首条 system; 派生号跨小时变化对超长会话等效新会话。
- **额度层不是代码缺陷**: 体检/转发都在消耗出口 IP 余额, 账号池换号与「优先粘性好号」是既有对策; 排查口诀见 [sigalgs 笔记](./2026-10-24-bun-clienthello-sigalgs-update.md)。v2 档在该出口池整体不可用, 待上游政策变化重测。

## Verification

```sh
go build ./... && go test ./internal/relay/ ./internal/client/ ./internal/utils/opencodeid/
```

- `TestEnsureOfficialOpencodeSystem*` / `TestShapeOpencodeFreeBodyInjectsOfficialSystem`: 注入/合并/幂等/体检模板直通。
- `TestBunClientHello*`: TLS 指纹 golden 锁定。
- 远程在线验证(2026-10-24): v1 档有额度出口上, 朴素请求+整形从 429 变 200; 哈希派生号修复后时间段与官方号同窗(旧号 429 的隔离实验在先)。
