# Agent Note: 免费档跨协议折叠先读完事件流

Status: implemented

## 问题

`TestOpencodeConvertedKeepsClientContract` 在 `anthropic/v1/stream=false` 子用例上随机 panic：`bufio.Scanner.Scan` 空指针，栈落在 `collapseOpencodeFreeStream` 继续读取窗口后的事件流时。CI 上稳定复现，导致 test 与 docker-publish 两个流水线失败。

根因是折叠路径复用了窗口预读的同一路真实事件流：`readStreamWindow` 读到首个内容事件就返回，折叠再继续读取。这中间存在一段暂停，异步事件流在暂停期间走到结束并关闭底层 SSE 解码器，后续 `Next` 访问已释放的解码器即空指针。

## 决定

- `sendConverted` 的折叠分支先用 `collectStreamEvents` 把真实事件流一次读完，再用 `streams.SliceStream` 回放给 `readStreamWindow` 做原有校验。
- 窗口校验、零输出校验、终态白名单、别名还原全部保持原样，只改变读取顺序。
- `collectStreamEvents` 不套用窗口预算：折叠本来就要聚合完整响应，窗口预算只约束首个内容信号前的结构帧。

## 备选方案

- **在第三方解码器里加空指针保护**：依赖不在本仓库内，升级即回退，且掩盖真实生命周期错误。
- **折叠时跳过窗口预读**：能避开暂停，但会丢掉 early-EOF、异常终态与零输出的校验口径，风险更大。
- **捕获 panic 转成错误**：会把程序缺陷伪装成可重试错误，违反禁止吞异常与造假的口径。

## 后果

- **收益**：折叠路径不再二次访问真实解码器，CI 的随机 panic 消失；校验语义与之前一致。
- **代价与已知上限**：折叠前会先缓存完整事件流，内存上限与原 `AggregateStreamChunks` 全量聚合一致，没有额外收紧。

## 验证

```sh
go test ./internal/relay/ -run 'TestOpencodeConvertedKeepsClientContract' -v
go test ./internal/relay/ -run 'TestOpencodeFree|TestCollapse' -v
go build ./... && go vet ./...
```