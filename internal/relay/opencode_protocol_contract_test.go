package relay

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/tidwall/gjson"
)

func ocContractClientTool(protocol, name string) string {
	schema := `{ "type": "object", "properties": { "diff": { "type": "string" }, "id": { "const": 9007199254740993 } }, "required": ["diff"], "additionalProperties": false }`
	fields := `"name": "` + name + `", "description": "client definition", `
	switch protocol {
	case model.UpstreamProtocolChat:
		return `{ "type": "function", "function": { ` + fields + `"parameters": ` + schema + `, "strict": true }, "x-client": { "keep": [1, false, null] } }`
	case model.UpstreamProtocolResponses:
		return `{ "type": "function", ` + fields + `"parameters": ` + schema + `, "strict": true, "x-client": { "keep": [1, false, null] } }`
	default:
		return `{ ` + fields + `"input_schema": ` + schema + `, "x-client": { "keep": [1, false, null] } }`
	}
}

func ocContractFindTool(body []byte, path, name string) (gjson.Result, bool) {
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get(path).String() == name {
			return tool, true
		}
	}
	return gjson.Result{}, false
}

func TestOpencodeProtocolContractPreservesClientTools(t *testing.T) {
	for _, lane := range []string{"v1", "v2"} {
		for _, protocol := range []string{model.UpstreamProtocolChat, model.UpstreamProtocolResponses, model.UpstreamProtocolAnthropic} {
			t.Run(lane+"/"+protocol, func(t *testing.T) {
				names := []string{"read", "edit", "patch", "shell", "ReadFile"}
				var definitions []string
				for _, name := range names {
					definitions = append(definitions, ocContractClientTool(protocol, name))
				}
				extra := `{ "type": "vendor_native", "name": "native", "vendor": { "schema": [1, 2] } }`
				definitions = append(definitions, extra)
				payload := `"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"diff\":\"read\"}"}}]},{"role":"tool","name":"read","tool_call_id":"call_1","content":"read"}],"tool_choice":{"type":"function","function":{"name":"read"}}`
				choicePath, historyPath := "tool_choice.function.name", "messages.1.tool_calls.0.function.name"
				switch protocol {
				case model.UpstreamProtocolResponses:
					payload = `"input":[{"type":"function_call","name":"read","call_id":"call_1","arguments":"{\"diff\":\"read\"}"}],"instructions":"client instruction","tool_choice":{"type":"function","name":"read"},"store":false,"prompt_cache_key":"keep"`
					choicePath, historyPath = "tool_choice.name", "input.0.name"
				case model.UpstreamProtocolAnthropic:
					payload = `"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read","input":{"diff":"read"}}]}],"system":"client system","tool_choice":{"type":"tool","name":"read"}`
					choicePath, historyPath = "tool_choice.name", "messages.0.content.0.name"
				}
				body := []byte(`{"model":"x-free",` + payload + `,"tools":[` + strings.Join(definitions, ",") + `]}`)
				out, changed, aliases := shapeOpencodeFreeBody(body, lane == "v1", protocol)
				if !changed || !json.Valid(out) {
					t.Fatalf("整形应输出合法改写正文: %s", out)
				}
				namePath, _, schemaPath := toolFieldPaths(protocol)
				for i, name := range names {
					wire, want := name, definitions[i]
					if protocol == model.UpstreamProtocolChat {
						wire = ocExpectWireName(name)
						want = strings.Replace(want, `"name": "`+name+`"`, `"name": "`+wire+`"`, 1)
						if aliases[wire] != name {
							t.Fatalf("缺少客户端映射 %s: %v", name, aliases)
						}
					}
					tool, found := ocContractFindTool(out, namePath, wire)
					if !found || tool.Raw != want {
						t.Fatalf("工具 %s 原文被改写或丢失: %s", name, tool.Raw)
					}
					if tool.Get(schemaPath).Raw != gjson.Get(definitions[i], schemaPath).Raw {
						t.Fatalf("工具 %s 参数定义改变", name)
					}
				}
				if !strings.Contains(gjson.GetBytes(out, "tools").Raw, extra) {
					t.Fatal("未知原生工具应原文保留")
				}
				wantReference := "read"
				if protocol == model.UpstreamProtocolChat {
					wantReference = ocExpectWireName("read")
					if len(aliases) != len(names) {
						t.Fatalf("映射不能包含官方或未知工具: %v", aliases)
					}
					if gjson.GetBytes(out, "messages.2.name").String() != wantReference || gjson.GetBytes(out, "messages.2.content").String() != "read" {
						t.Fatal("工具结果引用应改名，但结果文本不能改变")
					}
				} else {
					if len(aliases) != 0 || gjson.GetBytes(out, "stream_options").Exists() {
						t.Fatal("原生协议不能混入 Chat 别名或参数")
					}
					if lane == "v1" && gjson.GetBytes(out, "tools").Raw != gjson.GetBytes(body, "tools").Raw {
						t.Fatal("v1 原生 tools 数组必须保持原文")
					}
					if lane == "v2" {
						for _, core := range opencodeFreeCoreTools {
							if _, found := ocContractFindTool(out, namePath, core); !found {
								t.Fatalf("v2 原生缺少核心工具 %s", core)
							}
						}
					}
				}
				if gjson.GetBytes(out, choicePath).String() != wantReference || gjson.GetBytes(out, historyPath).String() != wantReference {
					t.Fatalf("选择和历史引用必须与定义一致: %s", out)
				}
				if protocol == model.UpstreamProtocolResponses {
					instructions := gjson.GetBytes(out, "instructions").String()
					if !strings.HasPrefix(instructions, officialPrefix(t, lane)) || !strings.HasSuffix(instructions, "client instruction") {
						t.Fatal("Responses instructions 应保留客户端并补官方前缀")
					}
					if gjson.GetBytes(out, "messages").Exists() || gjson.GetBytes(out, "prompt_cache_key").String() != "keep" || !gjson.GetBytes(out, "store").Exists() {
						t.Fatal("Responses 不能走 Chat 顶层改写")
					}
				}
				if protocol == model.UpstreamProtocolAnthropic && gjson.GetBytes(out, "system").String() != "client system" {
					t.Fatal("Anthropic system 不应改写")
				}
				again, changedAgain, aliasesAgain := shapeOpencodeFreeBody(out, lane == "v1", protocol)
				if changedAgain || !bytes.Equal(again, out) || len(aliasesAgain) != 0 {
					t.Fatalf("第二次整形必须幂等，不能重复摘要: changed=%v aliases=%v", changedAgain, aliasesAgain)
				}
			})
		}
	}
}

func TestOpencodeProtocolContractToolsBoundary(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"missing", ""},
		{"empty", `,"tools":[]`},
		{"null", `,"tools":null`},
		{"string", `,"tools":"bad"`},
		{"object", `,"tools":{"name":"read"}`},
		{"number", `,"tools":7`},
		{"invalid-items", `,"tools":[null,7,"bad",{}, {"type":"function","function":{"name":7}}]`},
	}
	for _, lane := range []string{"v1", "v2"} {
		for _, protocol := range []string{model.UpstreamProtocolChat, model.UpstreamProtocolResponses, model.UpstreamProtocolAnthropic} {
			for _, tc := range cases {
				t.Run(lane+"/"+protocol+"/"+tc.name, func(t *testing.T) {
					body := []byte(`{"model":"x-free"` + tc.field + `}`)
					out, _, aliases := shapeOpencodeFreeBody(body, lane == "v1", protocol)
					before, after := gjson.GetBytes(body, "tools"), gjson.GetBytes(out, "tools")
					if !json.Valid(out) || len(aliases) != 0 {
						t.Fatalf("异常工具不应生成映射: %s %v", out, aliases)
					}
					unchanged := before.Exists() && !before.IsArray() || lane == "v1" && protocol != model.UpstreamProtocolChat
					if unchanged {
						if before.Raw != after.Raw {
							t.Fatalf("tools 不应覆盖: before=%s after=%s", before.Raw, after.Raw)
						}
					} else if len(after.Array()) <= len(before.Array()) {
						t.Fatalf("应补缺失工具且保留原项: %s", after.Raw)
					}
					for _, item := range before.Array() {
						found := false
						for _, candidate := range after.Array() {
							found = found || item.Raw == candidate.Raw
						}
						if !found {
							t.Fatalf("异常工具元素不能静默丢弃: %s", item.Raw)
						}
					}
				})
			}
		}
	}
	for _, raw := range []string{`{`, `[]`, `null`, `"body"`, `7`} {
		body := []byte(raw)
		out, changed, aliases := shapeOpencodeFreeBody(body, true, model.UpstreamProtocolChat)
		if !bytes.Equal(body, out) || changed || len(aliases) != 0 {
			t.Fatalf("非法正文不应被拼补成请求: %s", out)
		}
	}
}

func TestOpencodeProtocolContractReferencesAndRestoration(t *testing.T) {
	wire := ocExpectWireName("read")
	aliases := map[string]string{wire: "read"}
	requests := []struct {
		name string
		body string
		path string
	}{
		{"chat-choice", `{"tool_choice":{"type":"function","function":{"name":"read"}}}`, "tool_choice.function.name"},
		{"chat-allowed", `{"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"read"}}]}}}`, "tool_choice.allowed_tools.tools.0.function.name"},
		{"responses-choice", `{"tool_choice":{"type":"function","name":"read"}}`, "tool_choice.name"},
		{"responses-allowed", `{"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"read"}]}}`, "tool_choice.tools.0.name"},
		{"anthropic-choice", `{"tool_choice":{"type":"tool","name":"read"}}`, "tool_choice.name"},
		{"chat-history", `{"messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":"read"}}]}]}`, "messages.0.tool_calls.0.function.name"},
		{"anthropic-history", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"read","input":{"text":"read"}}]}]}`, "messages.0.content.0.name"},
		{"responses-history", `{"input":[{"type":"function_call","name":"read","arguments":"read"}]}`, "input.0.name"},
		{"responses-custom-history", `{"input":[{"type":"custom_tool_call","name":"read","input":"read"}]}`, "input.0.name"},
	}
	for _, tc := range requests {
		t.Run(tc.name, func(t *testing.T) {
			out := rewriteToolReferences([]byte(tc.body), aliases)
			if gjson.GetBytes(out, tc.path).String() != wire {
				t.Fatalf("工具引用未改名: %s", out)
			}
			want := strings.Replace(tc.body, `"name":"read"`, `"name":"`+wire+`"`, 1)
			if string(out) != want {
				t.Fatalf("只能改写结构化工具名: %s", out)
			}
		})
	}
	responses := []struct {
		name   string
		body   string
		stream bool
	}{
		{"chat-body", `{"choices":[{"message":{"tool_calls":[{"function":{"name":"WIRE","arguments":"WIRE"}}],"content":"WIRE"}}]}`, false},
		{"anthropic-body", `{"content":[{"type":"tool_use","name":"WIRE","input":{"name":"WIRE"}},{"type":"text","text":"WIRE"}]}`, false},
		{"responses-body", `{"output":[{"type":"function_call","name":"WIRE","arguments":"WIRE"},{"type":"message","name":"WIRE"}]}`, false},
		{"responses-custom-body", `{"output":[{"type":"custom_tool_call","name":"WIRE","input":"WIRE"}]}`, false},
		{"chat-delta", `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"WIRE","arguments":"WIRE"}}],"content":"WIRE"}}]}`, true},
		{"anthropic-block", `{"type":"content_block_start","content_block":{"type":"tool_use","name":"WIRE","input":{"name":"WIRE"}}}`, true},
		{"responses-item", `{"type":"response.output_item.added","item":{"type":"function_call","name":"WIRE","arguments":"WIRE"}}`, true},
		{"responses-custom-item", `{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"WIRE","input":"WIRE"}}`, true},
		{"responses-completed", `{"type":"response.completed","response":{"output":[{"type":"function_call","name":"WIRE","arguments":"WIRE"}]}}`, true},
	}
	for _, lane := range []string{"v1", "v2"} {
		_, _, requestAliases := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":[`+ocContractClientTool(model.UpstreamProtocolChat, "read")+`]}`), lane == "v1", model.UpstreamProtocolChat)
		for _, tc := range responses {
			t.Run(lane+"/"+tc.name, func(t *testing.T) {
				body := []byte(strings.ReplaceAll(tc.body, "WIRE", wire))
				out := restoreToolAliasesInBody(body, requestAliases)
				if tc.stream {
					out = restoreToolAliasesInEvent(body, requestAliases)
				}
				want := strings.Replace(string(body), `"name":"`+wire+`"`, `"name":"read"`, 1)
				if string(out) != want {
					t.Fatalf("仅结构化工具名应还原，不能触碰参数和文本: %s", out)
				}
				if again := restoreToolAliasesInEvent(out, requestAliases); !bytes.Equal(again, out) {
					t.Fatal("工具名还原应幂等")
				}
			})
		}
	}
	for _, raw := range []string{`[DONE]`, `{"type":"content_block_delta","delta":{"partial_json":"clint_"}}`, `{"name":"` + wire + `","content":[{"type":"text","name":"` + wire + `"}]}`} {
		body := []byte(raw)
		if !bytes.Equal(restoreToolAliasesInEvent(body, aliases), body) {
			t.Fatalf("非工具字段不能还原: %s", body)
		}
	}
}

func TestOpencodeProtocolContractOfficialAndAliasIdempotence(t *testing.T) {
	templates, err := loadOcProbeTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"v1", "v2"} {
		t.Run(lane, func(t *testing.T) {
			original := []byte(`{"tools":` + string(templates[lane].Tools) + `}`)
			out, aliases := ensureOfficialToolset(original, lane)
			if !bytes.Equal(out, original) || len(aliases) != 0 {
				t.Fatal("官方原模板不应改写")
			}
			for _, name := range []string{"read", "edit"} {
				official, exists := ocContractFindTool(original, "function.name", name)
				if !exists {
					t.Fatalf("模板缺少 %s", name)
				}
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, []byte(official.Raw), "", "  "); err != nil {
					t.Fatal(err)
				}
				reused, reusedAliases := ensureOfficialToolset([]byte(`{"tools":[`+pretty.String()+`]}`), lane)
				if len(reusedAliases) != 0 || len(gjson.GetBytes(reused, "tools").Array()) != len(gjson.ParseBytes(templates[lane].Tools).Array()) {
					t.Fatal("仅排版不同的官方定义应复用")
				}
				duplicate, duplicateAliases := ensureOfficialToolset([]byte(`{"tools":[`+official.Raw+`,`+official.Raw+`]}`), lane)
				if len(duplicateAliases) != 0 || len(gjson.GetBytes(duplicate, "tools").Array()) != len(gjson.ParseBytes(templates[lane].Tools).Array()) {
					t.Fatal("重复的完整官方定义应复用")
				}
				withExtension := strings.TrimSuffix(official.Raw, "}") + `,"x-client":true}`
				extended, extendedAliases := ensureOfficialToolset([]byte(`{"tools":[`+withExtension+`]}`), lane)
				client, found := ocContractFindTool(extended, "function.name", ocExpectWireName(name))
				if !found || !client.Get("x-client").Bool() || extendedAliases[ocExpectWireName(name)] != name {
					t.Fatal("官方同名扩展不能被模板吞掉")
				}
			}
			wire := ocExpectWireName("read")
			clients := []string{ocContractClientTool(model.UpstreamProtocolChat, "read"), ocContractClientTool(model.UpstreamProtocolChat, wire), ocContractClientTool(model.UpstreamProtocolChat, "clint_custom")}
			mixed := []byte(`{"tools":` + strings.TrimSuffix(string(templates[lane].Tools), "]") + `,` + strings.Join(clients, ",") + `],"tool_choice":{"type":"function","function":{"name":"read"}}}`)
			first, firstAliases := ensureOfficialToolset(mixed, lane)
			if firstAliases[wire+"_"] != "read" || firstAliases[ocExpectWireName("clint_custom")] != "clint_custom" || len(firstAliases) != 2 {
				t.Fatalf("已存在摘要名必须预占且不重复摘要: %v", firstAliases)
			}
			if gjson.GetBytes(first, "tool_choice.function.name").String() != wire+"_" {
				t.Fatal("碰撞后引用必须指向实际出站名")
			}
			if len(gjson.GetBytes(first, "tools").Array()) != len(gjson.ParseBytes(templates[lane].Tools).Array())+len(clients) {
				t.Fatal("官方同名客户工具不能丢弃")
			}
			second, secondAliases := ensureOfficialToolset(first, lane)
			if !bytes.Equal(first, second) || len(secondAliases) != 0 {
				t.Fatalf("重复整形不能更换摘要或映射: %v", secondAliases)
			}
			_, nextAliases := ensureOfficialToolset([]byte(`{"tools":[`+ocContractClientTool(model.UpstreamProtocolChat, "fresh_tool")+`]}`), lane)
			if len(nextAliases) != 1 || nextAliases[ocExpectWireName("fresh_tool")] != "fresh_tool" || firstAliases[wire+"_"] != "read" {
				t.Fatal("别名映射不能跨请求泄漏或复用")
			}
		})
	}
}
