package relay

import (
	"testing"

	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
)

func officialTestTargets(t *testing.T) map[string]ocOfficialToolTarget {
	t.Helper()
	targets := buildOfficialToolTargets([]byte(`{"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"number"},"limit":{"type":"number"},"pages":{"type":"string"}},"required":["file_path"],"additionalProperties":false}}}]}`))
	if targets["read"].clientName != "Read" {
		t.Fatalf("read 目标缺失: %+v", targets)
	}
	return targets
}

func collectOfficialStream(t *testing.T, stream *officialToolStream) []*httpclient.StreamEvent {
	t.Helper()
	var out []*httpclient.StreamEvent
	for stream.Next() {
		out = append(out, stream.Current())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("读取流失败: %v", err)
	}
	return out
}

func TestBuildOfficialToolTargetsAndAdaptArguments(t *testing.T) {
	targets := buildOfficialToolTargets([]byte(`{"tools":[
		{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"number"},"limit":{"type":"number"},"pages":{"type":"string"}},"required":["file_path"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"},"run_in_background":{"type":"boolean"}},"required":["command"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"todo_write","parameters":{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string"}},"required":["content","status"],"additionalProperties":false}}},"required":["todos"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"WebFetch","parameters":{"type":"object","properties":{"url":{"type":"string"},"prompt":{"type":"string"}},"required":["url","prompt"],"additionalProperties":false}}}
	]}`))
	if targets["read"].clientName != "Read" {
		t.Fatalf("read 目标异常: %+v", targets)
	}
	if targets["bash"].clientName != "Bash" {
		t.Fatalf("bash 目标异常: %+v", targets)
	}
	if targets["todowrite"].clientName != "todo_write" {
		t.Fatalf("todowrite 目标异常: %+v", targets)
	}

	readArgs := adaptOfficialToolArguments("read", `{"filePath":"C:\\Windows\\win.ini","offset":1,"pages":"1-2","unknown":true}`, targets["read"])
	if got := gjson.Get(readArgs, "file_path").String(); got != `C:\Windows\win.ini` {
		t.Fatalf("read 参数键未适配: %s", readArgs)
	}
	if gjson.Get(readArgs, "filePath").Exists() || gjson.Get(readArgs, "unknown").Exists() {
		t.Fatalf("read 未声明参数未丢弃: %s", readArgs)
	}
	if got := gjson.Get(readArgs, "offset").Int(); got != 1 {
		t.Fatalf("read offset 丢失: %s", readArgs)
	}

	bashArgs := adaptOfficialToolArguments("bash", `{"command":"ls","workdir":"D:\\x","timeout":5}`, targets["bash"])
	if got := gjson.Get(bashArgs, "command").String(); got != "ls" {
		t.Fatalf("bash command 丢失: %s", bashArgs)
	}
	if gjson.Get(bashArgs, "workdir").Exists() {
		t.Fatalf("bash 未声明 workdir 未丢弃: %s", bashArgs)
	}

	editTargets := buildOfficialToolTargets([]byte(`{"tools":[{"type":"function","function":{"name":"Edit","parameters":{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["file_path","old_string","new_string"],"additionalProperties":false}}}]}`))
	editArgs := adaptOfficialToolArguments("edit", `{"filePath":"C:\\x","oldString":"a","newString":"b","replaceAll":true,"unknown":1}`, editTargets["edit"])
	if got := gjson.Get(editArgs, "file_path").String(); got != `C:\x` {
		t.Fatalf("edit file_path 未适配: %s", editArgs)
	}
	if gjson.Get(editArgs, "old_string").String() != "a" || gjson.Get(editArgs, "new_string").String() != "b" {
		t.Fatalf("edit 新旧内容未适配: %s", editArgs)
	}
	if !gjson.Get(editArgs, "replace_all").Bool() || gjson.Get(editArgs, "unknown").Exists() {
		t.Fatalf("edit 替换标志或未知键处理异常: %s", editArgs)
	}

	todoArgs := adaptOfficialToolArguments("todowrite", `{"todos":[{"content":"x","status":"pending","priority":"high"}]}`, targets["todowrite"])
	if gjson.Get(todoArgs, "todos.0.priority").Exists() {
		t.Fatalf("嵌套 priority 未丢弃: %s", todoArgs)
	}
	if got := gjson.Get(todoArgs, "todos.0.content").String(); got != "x" {
		t.Fatalf("嵌套 content 丢失: %s", todoArgs)
	}

	fetchArgs := adaptOfficialToolArguments("webfetch", `{"url":"https://example.com"}`, targets["webfetch"])
	if got := gjson.Get(fetchArgs, "prompt").String(); got != ocWebFetchDefaultPrompt {
		t.Fatalf("webfetch 默认 prompt 缺失: %s", fetchArgs)
	}

	pwshTargets := buildOfficialToolTargets([]byte(`{"tools":[{"type":"function","function":{"name":"pwsh","parameters":{"type":"object","properties":{"command":{"type":"string"},"workdir":{"type":"string"},"timeout":{"type":"number"}},"required":["command"],"additionalProperties":false}}}]}`))
	if pwshTargets["bash"].clientName != "pwsh" {
		t.Fatalf("DSH pwsh 目标异常: %+v", pwshTargets)
	}
	pwshArgs := adaptOfficialToolArguments("bash", `{"command":"ls","workdir":"D:\\x","background":true}`, pwshTargets["bash"])
	if got := gjson.Get(pwshArgs, "workdir").String(); got != `D:\x` {
		t.Fatalf("pwsh workdir 丢失: %s", pwshArgs)
	}
	if gjson.Get(pwshArgs, "background").Exists() {
		t.Fatalf("pwsh 未声明 background 未丢弃: %s", pwshArgs)
	}
}

func TestAdaptOfficialToolArgumentsKeepsArgsWithoutProperties(t *testing.T) {
	targets := buildOfficialToolTargets([]byte(`{"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}]}`))
	target, ok := targets["bash"]
	if !ok {
		t.Fatalf("bash 目标缺失: %+v", targets)
	}
	if !target.hasSchema || len(target.properties) != 0 {
		t.Fatalf("测试前置异常: hasSchema=%v properties=%d", target.hasSchema, len(target.properties))
	}
	const args = `{"command":"ls","timeout":5}`
	if got := adaptOfficialToolArguments("bash", args, target); got != args {
		t.Fatalf("无 properties 时参数被清空或改写: %s", got)
	}
}

func TestRestoreOfficialToolCallsInBody(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"1","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"C:\\\\x\",\"offset\":1,\"unknown\":true}"}}]}}]}`)
	out := restoreOfficialToolCallsInBody(body, officialTestTargets(t))
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "Read" {
		t.Fatalf("非流式工具名未还原: %s", out)
	}
	args := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String()
	if got := gjson.Get(args, "file_path").String(); got != `C:\x` {
		t.Fatalf("非流式参数未适配: %s", args)
	}
	if gjson.Get(args, "filePath").Exists() || gjson.Get(args, "unknown").Exists() {
		t.Fatalf("非流式未声明参数未丢弃: %s", args)
	}
}

func TestOfficialToolStreamBuffersSplitCalls(t *testing.T) {
	stream := &officialToolStream{
		inner: &fakeStream{events: []*httpclient.StreamEvent{
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`),
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"re","arguments":"{\"file"}}]}}]}`),
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"ad","arguments":"Path\":\"C:\\\\x\"}"}}]}}]}`),
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		}},
		official: officialTestTargets(t),
	}
	out := collectOfficialStream(t, stream)
	if len(out) != 3 {
		t.Fatalf("流式事件数异常: %d", len(out))
	}
	name := gjson.GetBytes(out[1].Data, "choices.0.delta.tool_calls.0.function.name").String()
	if name != "Read" {
		t.Fatalf("流式工具名未还原: %s", out[1].Data)
	}
	args := gjson.GetBytes(out[1].Data, "choices.0.delta.tool_calls.0.function.arguments").String()
	if got := gjson.Get(args, "file_path").String(); got != `C:\x` {
		t.Fatalf("流式参数未适配: %s", args)
	}
	if gjson.GetBytes(out[1].Data, "choices.0.delta.tool_calls.0.function.arguments.filePath").Exists() {
		t.Fatalf("流式参数键未替换: %s", out[1].Data)
	}
	if got := gjson.GetBytes(out[2].Data, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("终止原因丢失: %s", out[2].Data)
	}
	if gjson.GetBytes(out[2].Data, "choices.0.delta.tool_calls").Exists() {
		t.Fatalf("终止事件仍带工具分片: %s", out[2].Data)
	}
}

func TestOfficialToolStreamFlushesAtEOF(t *testing.T) {
	stream := &officialToolStream{
		inner: &fakeStream{events: []*httpclient.StreamEvent{
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"C:\\\\x\"}"}}]}}]}`),
		}},
		official: officialTestTargets(t),
	}
	out := collectOfficialStream(t, stream)
	if len(out) != 1 {
		t.Fatalf("EOF 未刷新工具调用: %d", len(out))
	}
	if got := gjson.GetBytes(out[0].Data, "choices.0.delta.tool_calls.0.function.name").String(); got != "Read" {
		t.Fatalf("EOF 工具名未还原: %s", out[0].Data)
	}
}

func TestOfficialToolStreamKeepsAliasArguments(t *testing.T) {
	stream := &officialToolStream{
		inner: &fakeStream{events: []*httpclient.StreamEvent{
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"clint_patcher","arguments":"{\"filePath\":\"x\"}"}}]}}]}`),
			sseEvent(`{"id":"chunk","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		}},
		aliases:  map[string]string{"clint_patcher": "patcher"},
		official: officialTestTargets(t),
	}
	out := collectOfficialStream(t, stream)
	if len(out) != 2 {
		t.Fatalf("别名流事件数异常: %d", len(out))
	}
	if got := gjson.GetBytes(out[0].Data, "choices.0.delta.tool_calls.0.function.name").String(); got != "patcher" {
		t.Fatalf("别名未还原: %s", out[0].Data)
	}
	args := gjson.GetBytes(out[0].Data, "choices.0.delta.tool_calls.0.function.arguments").String()
	if got := gjson.Get(args, "filePath").String(); got != "x" {
		t.Fatalf("别名参数不应改写: %s", args)
	}
}
