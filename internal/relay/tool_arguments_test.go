package relay

import (
	"errors"
	"testing"

	"github.com/looplj/axonhub/llm"
)

func TestValidateToolCallArguments(t *testing.T) {
	valid := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"edit","arguments":"{\"filePath\":\"x\"}"}}]}}]}`)
	if err := validateToolCallArguments(llm.APIFormatOpenAIChatCompletion, valid); err != nil {
		t.Fatalf("合法参数不应拒绝: %v", err)
	}

	invalid := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"edit","arguments":"{\"filePath\":\"x\""}}]}}]}`)
	if err := validateToolCallArguments(llm.APIFormatOpenAIChatCompletion, invalid); !errors.Is(err, errInvalidToolCallArguments) {
		t.Fatalf("非法参数应返回哨兵错误, 得到: %v", err)
	}

	empty := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"edit","arguments":""}}]}}]}`)
	if err := validateToolCallArguments(llm.APIFormatOpenAIChatCompletion, empty); err != nil {
		t.Fatalf("空参数应放行: %v", err)
	}

	legacy := []byte(`{"choices":[{"message":{"function_call":{"name":"edit","arguments":"not-json"}}}]}`)
	if err := validateToolCallArguments(llm.APIFormatOpenAIChatCompletion, legacy); !errors.Is(err, errInvalidToolCallArguments) {
		t.Fatalf("旧式 function_call 非法参数应拒绝, 得到: %v", err)
	}

	if err := validateToolCallArguments(llm.APIFormatAnthropicMessage, invalid); err != nil {
		t.Fatalf("非 OpenAI Chat 协议不应校验: %v", err)
	}
}
