package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestApplyOpencodeAlias_PromptScope(t *testing.T) {
	cases := []struct {
		name      string
		format    llm.APIFormat
		body      string
		changed   []string
		untouched []string
	}{
		{
			name: "chat", format: llm.APIFormatOpenAIChatCompletion,
			body: `{"model":"opencode","messages":[{"role":"system","content":"opencode"},{"role":"developer","content":[{"type":"text","text":"opencode"},{"type":"image_url","image_url":{"url":"opencode"}}]},{"role":"user","content":"opencode"},{"role":"assistant","content":"opencode","tool_calls":[{"function":{"name":"opencode","arguments":"opencode"}}]},{"role":"tool","content":"opencode"}],"tools":[{"type":"function","function":{"name":"opencode","description":"opencode","parameters":{"opencode":"opencode"}}}],"metadata":{"opencode":"opencode"}}`,
			changed: []string{"messages.0.content", "messages.1.content.0.text"},
			untouched: []string{"model", "messages.1.content.1", "messages.2", "messages.3", "messages.4", "tools", "metadata"},
		},
		{
			name: "anthropic", format: llm.APIFormatAnthropicMessage,
			body: `{"model":"opencode","system":[{"type":"text","text":"opencode","cache_control":{"type":"opencode"}},{"type":"image","text":"opencode"}],"messages":[{"role":"user","content":"opencode"},{"role":"assistant","content":[{"type":"tool_use","name":"opencode","input":{"text":"opencode"}}]}],"tools":[{"name":"opencode","description":"opencode","input_schema":{"title":"opencode"}}]}`,
			changed: []string{"system.0.text"},
			untouched: []string{"model", "system.0.cache_control", "system.1", "messages", "tools"},
		},
		{
			name: "responses", format: llm.APIFormatOpenAIResponse,
			body: `{"model":"opencode","instructions":"opencode","input":[{"role":"system","content":"opencode"},{"type":"message","role":"developer","content":[{"type":"input_text","text":"opencode"},{"type":"input_image","image_url":"opencode"}]},{"role":"user","content":[{"type":"input_text","text":"opencode"}]},{"role":"assistant","content":[{"type":"output_text","text":"opencode"}]},{"type":"function_call","role":"system","name":"opencode","arguments":"opencode","content":"opencode"},{"type":"function_call_output","output":"opencode"}],"tools":[{"type":"function","name":"opencode","description":"opencode","parameters":{"title":"opencode"}}]}`,
			changed: []string{"instructions", "input.0.content", "input.1.content.0.text"},
			untouched: []string{"model", "input.1.content.1", "input.2", "input.3", "input.4", "input.5", "tools"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			out, state, err := applyOpencodeAlias(body, tc.format)
			if err != nil || state == nil {
				t.Fatalf("alias state=%v err=%v", state, err)
			}
			if len(state.aliases) != 1 {
				t.Fatalf("repeated name created %d aliases", len(state.aliases))
			}
			for _, path := range tc.changed {
				alias := gjson.GetBytes(out, path).String()
				if !strings.HasPrefix(alias, "opencode_") || state.aliases[alias] != "opencode" {
					t.Errorf("prompt %s not aliased: %q", path, alias)
				}
			}
			for _, path := range tc.untouched {
				if got, want := gjson.GetBytes(out, path).Raw, gjson.GetBytes(body, path).Raw; got != want {
					t.Errorf("non-prompt %s changed: %s != %s", path, got, want)
				}
			}
			if string(body) != tc.body {
				t.Fatal("request source slice mutated")
			}
		})
	}
}

func TestApplyOpencodeAlias_NoMatchSameSlice(t *testing.T) {
	for _, tc := range []struct {
		format llm.APIFormat
		body   string
	}{
		{llm.APIFormatOpenAIChatCompletion, `{"messages":[{"role":"user","content":"opencode"},{"role":"system","content":"myopencode opencode_x opencode2 deepseek grok"}]}`},
		{llm.APIFormatAnthropicMessage, `{"system":"ordinary prompt","messages":[{"role":"user","content":"workbuddy"}]}`},
		{llm.APIFormatOpenAIResponse, `{"instructions":"ordinary prompt","input":"zcode"}`},
		{llm.APIFormat("unknown"), `not JSON`},
	} {
		body := []byte(tc.body)
		out, state, err := applyOpencodeAlias(body, tc.format)
		if err != nil || state != nil || len(out) != len(body) || &out[0] != &body[0] {
			t.Fatalf("no-match changed slice/state: format=%s state=%v err=%v", tc.format, state, err)
		}
	}
}

func TestApplyOpencodeAlias_NamesCaseEscapesAndChinese(t *testing.T) {
	original := "deepseek HARNESS | DEEPSEEK harness | opencode | OpenCode | grok build | GROK BUILD | liveagent | NovaVeilAgent | WORKBUDDY | Zcode | opencode"
	body := []byte(`{"messages":[{"role":"system","content":"` + original + `"},{"role":"developer","content":"你是opencode助手，请使用workbuddy完成。\\\"\n\u006fpen\u0063ode"}],"stream":true,"temperature":0,"metadata":null}`)
	out, state, err := applyOpencodeAlias(body, llm.APIFormatOpenAIChatCompletion)
	if err != nil || state == nil {
		t.Fatalf("alias: %v", err)
	}
	if len(state.aliases) != 11 {
		t.Fatalf("case-preserving aliases=%d, want 11", len(state.aliases))
	}
	for _, path := range []string{"messages.0.content", "messages.1.content"} {
		encoded := gjson.GetBytes(out, path).String()
		want := gjson.GetBytes(body, path).String()
		if got := state.replacer.Replace(encoded); got != want {
			t.Fatalf("round trip %s=%q want %q", path, got, want)
		}
		if encoded == want {
			t.Fatalf("prompt %s unchanged", path)
		}
	}
	for alias, originalName := range state.aliases {
		if len(alias) != len("opencode_")+32 {
			t.Errorf("unexpected alias %q", alias)
		}
		if !strings.EqualFold(originalName, "opencode") && strings.Contains(strings.ToLower(alias), strings.ToLower(originalName)) {
			t.Errorf("alias discloses name %q", originalName)
		}
	}
	again, _, err := applyOpencodeAlias(body, llm.APIFormatOpenAIChatCompletion)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("stable prompt changed across retries: %v", err)
	}
}

func TestApplyOpencodeAlias_CollisionAndIsolation(t *testing.T) {
	alias, first := identityTestAlias(t, "WorkBuddy")
	body := []byte(`{"messages":[{"role":"system","content":"WorkBuddy"},{"role":"user","content":"\u006f` + alias[1:] + `"}],"metadata":{"flag":true}}`)
	out, second, err := applyOpencodeAlias(body, llm.APIFormatOpenAIChatCompletion)
	if err != nil || second == nil {
		t.Fatalf("collision alias: %v", err)
	}
	newAlias := gjson.GetBytes(out, "messages.0.content").String()
	if newAlias == alias || second.aliases[alias] != "" || second.aliases[newAlias] != "WorkBuddy" {
		t.Fatal("existing literal or escaped alias captured by current request")
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != alias {
		t.Fatalf("existing user alias changed: %q", got)
	}
	if got := first.replacer.Replace(newAlias); got != newAlias {
		t.Fatal("first request acquired second request alias")
	}
	if got := second.replacer.Replace(alias); got != alias {
		t.Fatal("second request restored first request alias")
	}
	otherAlias, other := identityTestAlias(t, "Zcode")
	if first.replacer.Replace(otherAlias) != otherAlias || other.replacer.Replace(alias) != alias {
		t.Fatal("different requests share restoration state")
	}
}

func TestRestoreOpencodeIdentity_NonStreamWhitelist(t *testing.T) {
	alias, state := identityTestAlias(t, "NovaVeilAgent")
	cases := []struct {
		name      string
		format    llm.APIFormat
		body      string
		changed   []string
		untouched []string
	}{
		{
			name: "chat", format: llm.APIFormatOpenAIChatCompletion,
			body: `{"model":"@A@","choices":[{"message":{"content":"@A@","reasoning_content":"@A@","reasoning":"@A@","tool_calls":[{"function":{"name":"@A@","arguments":"@A@"}}]}},{"message":{"content":[{"type":"text","text":"@A@"},{"type":"image","text":"@A@"}]}}]}`,
			changed: []string{"choices.0.message.content", "choices.0.message.reasoning_content", "choices.0.message.reasoning", "choices.1.message.content.0.text"},
			untouched: []string{"model", "choices.0.message.tool_calls.0.function.name", "choices.0.message.tool_calls.0.function.arguments", "choices.1.message.content.1.text"},
		},
		{
			name: "anthropic", format: llm.APIFormatAnthropicMessage,
			body: `{"model":"@A@","content":[{"type":"text","text":"@A@"},{"type":"thinking","thinking":"@A@","signature":"@A@"},{"type":"tool_use","name":"@A@","input":{"text":"@A@"}},{"type":"redacted_thinking","data":"@A@"}]}`,
			changed: []string{"content.0.text", "content.1.thinking"},
			untouched: []string{"model", "content.1.signature", "content.2.name", "content.2.input.text", "content.3.data"},
		},
		{
			name: "responses", format: llm.APIFormatOpenAIResponse,
			body: `{"model":"@A@","output_text":"@A@","output":[{"type":"message","content":[{"type":"output_text","text":"@A@"},{"type":"image","text":"@A@"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"@A@"}],"content":[{"type":"reasoning_text","text":"@A@"}],"encrypted_content":"@A@"},{"type":"function_call","name":"@A@","arguments":"@A@"}]}`,
			changed: []string{"output_text", "output.0.content.0.text", "output.1.summary.0.text", "output.1.content.0.text"},
			untouched: []string{"model", "output.0.content.1.text", "output.1.encrypted_content", "output.2.name", "output.2.arguments"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(strings.ReplaceAll(tc.body, "@A@", alias))
			resp := &upstreamResponse{body: body}
			if err := applyOpencodeIdentityRestore(resp, tc.format, state); err != nil {
				t.Fatal(err)
			}
			for _, path := range tc.changed {
				if got := gjson.GetBytes(resp.body, path).String(); got != "NovaVeilAgent" {
					t.Errorf("text %s=%q", path, got)
				}
			}
			for _, path := range tc.untouched {
				if got := gjson.GetBytes(resp.body, path).String(); got != alias {
					t.Errorf("non-text %s=%q", path, got)
				}
			}
			if !bytes.Equal(body, []byte(strings.ReplaceAll(tc.body, "@A@", alias))) {
				t.Fatal("source body mutated")
			}
		})
	}
	body := []byte(`{"choices":[{"message":{"content":"opencode opencode_unknown \u006f` + alias[1:] + `"}}]}`)
	out, err := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	if err != nil || gjson.GetBytes(out, "choices.0.message.content").String() != "opencode opencode_unknown NovaVeilAgent" {
		t.Fatalf("escaped restoration: %s, %v", out, err)
	}
}

func TestOpencodeIdentity_MultipleNamesRoundTrip(t *testing.T) {
	masked, state := identityTestAlias(t, "LiveAgent | OpenCode | WorkBuddy")
	want := "LiveAgent | OpenCode | WorkBuddy / opencode / opencode_unknown"
	payload := masked + " / opencode / opencode_unknown"
	body := identityTestFrame(t, `{"choices":[{"message":{"content":@TEXT@}}]}`, payload).Data
	out, err := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	if err != nil || gjson.GetBytes(out, "choices.0.message.content").String() != want {
		t.Fatalf("non-stream multiple names: %s err=%v", out, err)
	}
	var events []*httpclient.StreamEvent
	for i := 0; i < len(payload); i++ {
		events = append(events, identityTestFrame(t, `{"choices":[{"index":0,"delta":{"content":@TEXT@}}]}`, payload[i:i+1]))
	}
	resp := &upstreamResponse{events: &identityTestStream{events: events}}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err != nil {
		t.Fatal(err)
	}
	frames, err := identityTestCollect(resp)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, frame := range frames {
		text.WriteString(gjson.GetBytes(frame.Data, "choices.0.delta.content").String())
	}
	if text.String() != want {
		t.Fatalf("stream multiple names=%q", text.String())
	}
}

func TestOpencodeIdentity_InvalidAndNoop(t *testing.T) {
	_, state := identityTestAlias(t, "WorkBuddy")
	for _, body := range []string{`{"messages":`, `[]`, `null`} {
		if _, _, err := applyOpencodeAlias([]byte(body), llm.APIFormatOpenAIChatCompletion); err == nil {
			t.Errorf("accepted invalid request %s", body)
		}
		if _, err := restoreOpencodeAliasNonStream([]byte(body), llm.APIFormatOpenAIChatCompletion, state); err == nil {
			t.Errorf("accepted invalid response %s", body)
		}
	}
	body := []byte(`{"choices":[{"message":{"content":"opencode and opencode_unknown"}}]}`)
	for _, current := range []*opencodeAliasState{nil, state} {
		out, err := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, current)
		if err != nil || &out[0] != &body[0] {
			t.Fatalf("no-op changed source: %v", err)
		}
	}
}

func TestOpencodeIdentity_StreamEveryByte(t *testing.T) {
	alias, state := identityTestAlias(t, "DeepSeek HARNESS")
	cases := []struct {
		name     string
		format   llm.APIFormat
		delta    string
		path     string
		terminal string
	}{
		{"chat-content", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":3,"delta":{"content":@TEXT@}}]}`, "choices.0.delta.content", `[DONE]`},
		{"chat-reasoning", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":3,"delta":{"reasoning_content":@TEXT@}}]}`, "choices.0.delta.reasoning_content", `[DONE]`},
		{"chat-block", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":3,"delta":{"content":[{"index":8,"type":"text","text":@TEXT@}]}}]}`, "choices.0.delta.content.0.text", `[DONE]`},
		{"anthropic-text", llm.APIFormatAnthropicMessage, `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":@TEXT@}}`, "delta.text", `{"type":"message_stop"}`},
		{"anthropic-thinking", llm.APIFormatAnthropicMessage, `{"type":"content_block_delta","index":3,"delta":{"type":"thinking_delta","thinking":@TEXT@}}`, "delta.thinking", `{"type":"message_stop"}`},
		{"responses-text", llm.APIFormatOpenAIResponse, `{"type":"response.output_text.delta","item_id":"item3","output_index":3,"content_index":8,"delta":@TEXT@}`, "delta", `{"type":"response.completed","response":{"status":"completed"}}`},
		{"responses-reasoning", llm.APIFormatOpenAIResponse, `{"type":"response.reasoning_text.delta","item_id":"item3","output_index":3,"content_index":8,"delta":@TEXT@}`, "delta", `{"type":"response.completed","response":{"status":"completed"}}`},
		{"responses-summary", llm.APIFormatOpenAIResponse, `{"type":"response.reasoning_summary_text.delta","item_id":"item3","output_index":3,"summary_index":8,"delta":@TEXT@}`, "delta", `{"type":"response.completed","response":{"status":"completed"}}`},
	}
	payload := "before [" + alias + "] \"\\\n opencode opencode_unknown " + alias[:len(alias)-1]
	want := "before [DeepSeek HARNESS] \"\\\n opencode opencode_unknown " + alias[:len(alias)-1]
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []*httpclient.StreamEvent
			for i := range payload {
				events = append(events, identityTestFrame(t, tc.delta, payload[i:i+1]))
			}
			events = append(events, identityTestRawFrame(tc.terminal))
			source := &identityTestStream{events: events[9:]}
			resp := &upstreamResponse{window: events[:9], events: source}
			if err := applyOpencodeIdentityRestore(resp, tc.format, state); err != nil {
				t.Fatal(err)
			}
			out, err := identityTestCollect(resp)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for i, event := range out {
				if analyzeStreamEvent(tc.format, event).terminal && i != len(out)-1 {
					t.Fatal("tail emitted after terminal event")
				}
				text.WriteString(gjson.GetBytes(event.Data, tc.path).String())
			}
			if text.String() != want {
				t.Fatalf("stream=%q want=%q", text.String(), want)
			}
			if string(events[0].Data) != string(identityTestFrame(t, tc.delta, payload[:1]).Data) {
				t.Fatal("source event mutated")
			}
			if source.nextCalls != len(source.events) {
				t.Fatalf("read past terminal: %d calls", source.nextCalls)
			}
		})
	}
}

func TestOpencodeIdentity_ChatChannelsAndTools(t *testing.T) {
	alias, state := identityTestAlias(t, "WorkBuddy")
	cut := len(alias) / 2
	frames := []string{
		`{"choices":[{"index":5,"delta":{"content":"` + alias[:cut] + `","reasoning_content":"` + alias[:cut] + `"}},{"index":2,"delta":{"content":"` + alias[:cut] + `"}}]}`,
		`{"choices":[{"index":2,"delta":{"content":"` + alias[cut:] + `"}},{"index":5,"delta":{"reasoning_content":"` + alias[cut:] + `","tool_calls":[{"index":0,"function":{"name":"` + alias + `","arguments":"` + alias + `"}}]}}]}`,
		`{"choices":[{"index":5,"delta":{"content":"` + alias[cut:] + `"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}
	var events []*httpclient.StreamEvent
	for _, frame := range frames {
		events = append(events, identityTestRawFrame(frame))
	}
	resp := &upstreamResponse{events: &identityTestStream{events: events}}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err != nil {
		t.Fatal(err)
	}
	out, err := identityTestCollect(resp)
	if err != nil {
		t.Fatal(err)
	}
	text := make(map[string]string)
	for _, event := range out {
		for _, choice := range gjson.GetBytes(event.Data, "choices").Array() {
			for _, field := range []string{"content", "reasoning_content"} {
				key := fmt.Sprintf("%d:%s", choice.Get("index").Int(), field)
				text[key] += choice.Get("delta." + field).String()
			}
			for _, tool := range choice.Get("delta.tool_calls").Array() {
				if tool.Get("function.name").String() != alias || tool.Get("function.arguments").String() != alias {
					t.Fatal("tool payload restored")
				}
			}
		}
	}
	for _, key := range []string{"5:content", "5:reasoning_content", "2:content"} {
		if text[key] != "WorkBuddy" {
			t.Errorf("channel %s=%q", key, text[key])
		}
	}
}

func TestOpencodeIdentity_StreamToolPayloadsUntouched(t *testing.T) {
	alias, state := identityTestAlias(t, "WorkBuddy")
	cases := []struct {
		format llm.APIFormat
		frames []string
	}{
		{llm.APIFormatAnthropicMessage, []string{
			`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"@A@","name":"@A@","input":{"text":"@A@"}}}`,
			`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"@A@"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"@A@"}}`,
		}},
		{llm.APIFormatOpenAIResponse, []string{
			`{"type":"response.output_item.added","output_index":4,"item":{"type":"function_call","id":"@A@","name":"@A@","arguments":"@A@"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":4,"delta":"@A@"}`,
			`{"type":"response.function_call_arguments.done","output_index":4,"arguments":"@A@"}`,
			`{"type":"response.output_item.done","output_index":4,"item":{"type":"function_call","name":"@A@","arguments":"@A@"}}`,
		}},
	}
	for _, tc := range cases {
		var events []*httpclient.StreamEvent
		for _, frame := range tc.frames {
			events = append(events, identityTestRawFrame(strings.ReplaceAll(frame, "@A@", alias)))
		}
		resp := &upstreamResponse{events: &identityTestStream{events: events}}
		if err := applyOpencodeIdentityRestore(resp, tc.format, state); err != nil {
			t.Fatal(err)
		}
		out, err := identityTestCollect(resp)
		if err != nil || len(out) != len(events) {
			t.Fatalf("tool stream events=%d err=%v", len(out), err)
		}
		for i := range out {
			if !bytes.Equal(out[i].Data, events[i].Data) || out[i].Type != events[i].Type {
				t.Fatalf("tool stream payload changed: %s", out[i].Data)
			}
		}
	}
}

func TestOpencodeIdentity_IndependentBlocks(t *testing.T) {
	alias, state := identityTestAlias(t, "LiveAgent")
	cut := len(alias) / 2
	for _, format := range []llm.APIFormat{llm.APIFormatAnthropicMessage, llm.APIFormatOpenAIResponse} {
		t.Run(format.String(), func(t *testing.T) {
			var events []*httpclient.StreamEvent
			for _, half := range []string{alias[:cut], alias[cut:]} {
				for _, index := range []int{4, 9} {
					var frame string
					if format == llm.APIFormatAnthropicMessage {
						frame = fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":@TEXT@}}`, index)
					} else {
						frame = fmt.Sprintf(`{"type":"response.output_text.delta","output_index":2,"content_index":%d,"delta":@TEXT@}`, index)
					}
					events = append(events, identityTestFrame(t, frame, half))
				}
			}
			resp := &upstreamResponse{events: &identityTestStream{events: events}}
			if err := applyOpencodeIdentityRestore(resp, format, state); err != nil {
				t.Fatal(err)
			}
			out, err := identityTestCollect(resp)
			if err != nil {
				t.Fatal(err)
			}
			text := make(map[int64]string)
			for _, event := range out {
				if format == llm.APIFormatAnthropicMessage {
					text[gjson.GetBytes(event.Data, "index").Int()] += gjson.GetBytes(event.Data, "delta.text").String()
				} else {
					text[gjson.GetBytes(event.Data, "content_index").Int()] += gjson.GetBytes(event.Data, "delta").String()
				}
			}
			if text[4] != "LiveAgent" || text[9] != "LiveAgent" {
				t.Fatalf("blocks mixed: %v", text)
			}
		})
	}
}

func TestOpencodeIdentity_FlushBeforeBlockStop(t *testing.T) {
	alias, state := identityTestAlias(t, "WorkBuddy")
	partial := alias[:len(alias)-1]
	cases := []struct {
		format llm.APIFormat
		delta  string
		stop   string
		path   string
	}{
		{llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":6,"delta":{"content":@TEXT@}}]}`, `{"choices":[{"index":6,"delta":{},"finish_reason":"stop"}]}`, "choices.0.delta.content"},
		{llm.APIFormatAnthropicMessage, `{"type":"content_block_start","index":6,"content_block":{"type":"text","text":@TEXT@}}`, `{"type":"content_block_stop","index":6}`, "delta.text"},
		{llm.APIFormatOpenAIResponse, `{"type":"response.output_text.delta","output_index":6,"content_index":1,"delta":@TEXT@}`, `{"type":"response.content_part.done","output_index":6,"content_index":1,"part":{"type":"output_text","text":"x"}}`, "delta"},
	}
	for _, tc := range cases {
		resp := &upstreamResponse{window: []*httpclient.StreamEvent{identityTestFrame(t, tc.delta, partial), identityTestRawFrame(tc.stop)}, last: true}
		if err := applyOpencodeIdentityRestore(resp, tc.format, state); err != nil {
			t.Fatal(err)
		}
		out, err := identityTestCollect(resp)
		if err != nil || len(out) != 3 {
			t.Fatalf("flush events=%d err=%v", len(out), err)
		}
		if got := gjson.GetBytes(out[1].Data, tc.path).String(); got != partial {
			t.Fatalf("missing literal tail %q", got)
		}
		if string(out[2].Data) != tc.stop {
			t.Fatal("stop event changed or tail arrived too late")
		}
		if verdict := analyzeStreamEvent(tc.format, out[1]); verdict.terminal || verdict.finishSeen || verdict.inspectErr != nil {
			t.Fatalf("synthetic delta faked terminal: %+v", verdict)
		}
	}
}

func TestOpencodeIdentity_ResponsesSequenceAndSnapshots(t *testing.T) {
	alias, state := identityTestAlias(t, "Zcode")
	partial := alias[:len(alias)-1]
	first := identityTestFrame(t, `{"type":"response.output_text.delta","sequence_number":7,"item_id":"m","output_index":1,"content_index":2,"delta":@TEXT@}`, partial)
	last := identityTestRawFrame(`{"type":"response.completed","sequence_number":8,"response":{"status":"completed","model":"` + alias + `","output":[{"type":"message","content":[{"type":"output_text","text":"` + alias + `"}]},{"type":"function_call","name":"` + alias + `","arguments":"` + alias + `"}]}}`)
	resp := &upstreamResponse{window: []*httpclient.StreamEvent{first}, events: &identityTestStream{events: []*httpclient.StreamEvent{last}}}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIResponse, state); err != nil {
		t.Fatal(err)
	}
	out, err := identityTestCollect(resp)
	if err != nil || len(out) != 3 {
		t.Fatalf("sequence events=%d err=%v", len(out), err)
	}
	for i, event := range out {
		if got := gjson.GetBytes(event.Data, "sequence_number"); !got.Exists() || got.Int() != int64(7+i) {
			t.Errorf("event %d sequence=%s", i, got.Raw)
		}
	}
	if gjson.GetBytes(out[1].Data, "delta").String() != partial || gjson.GetBytes(out[1].Data, "item_id").String() != "m" {
		t.Fatal("synthetic Responses delta lost text or routing")
	}
	if gjson.GetBytes(out[2].Data, "response.output.0.content.0.text").String() != "Zcode" {
		t.Fatal("terminal text snapshot was not restored")
	}
	for _, path := range []string{"response.model", "response.output.1.name", "response.output.1.arguments"} {
		if gjson.GetBytes(out[2].Data, path).String() != alias {
			t.Fatalf("snapshot changed non-text %s", path)
		}
	}
	if gjson.GetBytes(last.Data, "sequence_number").Int() != 8 {
		t.Fatal("renumbering mutated source")
	}
}

func TestOpencodeIdentity_StreamFailuresAndClose(t *testing.T) {
	alias, state := identityTestAlias(t, "WorkBuddy")
	partial := alias[:13]
	readErr := errors.New("identity upstream truncated")
	closeErr := errors.New("identity upstream close failed")
	source := &identityTestStream{
		events: []*httpclient.StreamEvent{identityTestFrame(t, `{"choices":[{"index":1,"delta":{"content":@TEXT@}}]}`, partial)},
		err: readErr, closeErr: closeErr,
	}
	resp := &upstreamResponse{events: source}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err != nil {
		t.Fatal(err)
	}
	out, err := identityTestCollect(resp)
	if !errors.Is(err, readErr) || len(out) != 2 || gjson.GetBytes(out[1].Data, "choices.0.delta.content").String() != partial {
		t.Fatalf("truncated stream lost error or tail: events=%d err=%v", len(out), err)
	}
	for i := 0; i < 2; i++ {
		if err := resp.events.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("Close error lost: %v", err)
		}
	}
	if source.closeCalls != 1 || !errors.Is(resp.events.Err(), closeErr) || !errors.Is(resp.events.Err(), readErr) {
		t.Fatal("Close was repeated or errors were swallowed")
	}
	for _, failure := range []string{`{"choices":`, `{"error":{"message":"failed","type":"upstream"}}`, `{"choices":[{"index":1,"delta":{},"finish_reason":"network_error"}]}`} {
		source := &identityTestStream{events: []*httpclient.StreamEvent{identityTestFrame(t, `{"choices":[{"index":1,"delta":{"content":@TEXT@}}]}`, partial), identityTestRawFrame(failure)}}
		resp := &upstreamResponse{events: source}
		if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err != nil {
			t.Fatal(err)
		}
		out, err := identityTestCollect(resp)
		if err == nil {
			t.Fatalf("failure swallowed: %s", failure)
		}
		var text strings.Builder
		for _, event := range out {
			text.WriteString(gjson.GetBytes(event.Data, "choices.0.delta.content").String())
		}
		if text.String() != partial {
			t.Fatalf("failure lost tail: %q", text.String())
		}
	}
}

func TestOpencodeIdentity_StreamBounds(t *testing.T) {
	alias, state := identityTestAlias(t, "WorkBuddy")
	var events []*httpclient.StreamEvent
	for i := 0; i <= opencodeAliasMaxChannels; i++ {
		frame := fmt.Sprintf(`{"choices":[{"index":%d,"delta":{"content":@TEXT@}}]}`, i)
		events = append(events, identityTestFrame(t, frame, alias[:1]))
	}
	resp := &upstreamResponse{events: &identityTestStream{events: events}}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err != nil {
		t.Fatal(err)
	}
	for resp.events.Next() {
		restorer := resp.events.(*opencodeIdentityStream)
		if len(restorer.pending) > opencodeAliasMaxChannels {
			t.Fatal("pending channel bound exceeded")
		}
		for _, pending := range restorer.pending {
			if len(pending.text) >= len(alias) || len(pending.template.Data) > opencodeAliasMaxTemplateBytes {
				t.Fatal("pending suffix or template bound exceeded")
			}
		}
	}
	if err := resp.events.Err(); err == nil || !strings.Contains(err.Error(), "channel limit") {
		t.Fatalf("unbounded channels accepted: %v", err)
	}
	oversized := identityTestFrame(t, `{"choices":[{"index":0,"delta":{"content":@TEXT@}}]}`, alias[:1])
	oversized.LastEventID = strings.Repeat("x", opencodeAliasMaxTemplateBytes+1)
	resp = &upstreamResponse{window: []*httpclient.StreamEvent{oversized}, last: true}
	if err := applyOpencodeIdentityRestore(resp, llm.APIFormatOpenAIChatCompletion, state); err == nil || !strings.Contains(err.Error(), "template limit") {
		t.Fatalf("oversized routing template accepted: %v", err)
	}
}

func identityTestAlias(t *testing.T, original string) (string, *opencodeAliasState) {
	t.Helper()
	body, err := sjson.SetBytes([]byte(`{"messages":[{"role":"system","content":""}]}`), "messages.0.content", original)
	if err != nil {
		t.Fatal(err)
	}
	out, state, err := applyOpencodeAlias(body, llm.APIFormatOpenAIChatCompletion)
	if err != nil || state == nil {
		t.Fatalf("alias %q: state=%v err=%v", original, state, err)
	}
	return gjson.GetBytes(out, "messages.0.content").String(), state
}

func identityTestFrame(t *testing.T, template, text string) *httpclient.StreamEvent {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return identityTestRawFrame(strings.ReplaceAll(template, "@TEXT@", string(encoded)))
}

func identityTestRawFrame(body string) *httpclient.StreamEvent {
	return &httpclient.StreamEvent{Type: gjson.Get(body, "type").String(), Data: []byte(body)}
}

func identityTestCollect(resp *upstreamResponse) ([]*httpclient.StreamEvent, error) {
	out := append([]*httpclient.StreamEvent(nil), resp.window...)
	for resp.events.Next() {
		event := resp.events.Current()
		if event != resp.events.Current() {
			return nil, errors.New("Current advances stream state")
		}
		out = append(out, event)
	}
	return out, resp.events.Err()
}

type identityTestStream struct {
	events     []*httpclient.StreamEvent
	index      int
	nextCalls  int
	closeCalls int
	err        error
	closeErr   error
	exhausted  bool
}

func (s *identityTestStream) Next() bool {
	s.nextCalls++
	if s.index == len(s.events) {
		s.exhausted = true
		return false
	}
	s.index++
	return true
}

func (s *identityTestStream) Current() *httpclient.StreamEvent {
	if s.index == 0 || s.index > len(s.events) {
		return nil
	}
	return s.events[s.index-1]
}

func (s *identityTestStream) Err() error {
	if s.exhausted {
		return s.err
	}
	return nil
}

func (s *identityTestStream) Close() error {
	s.closeCalls++
	return s.closeErr
}
