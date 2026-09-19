package relay

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"

	"github.com/looplj/axonhub/llm/httpclient"
)

// 真实环境下的 socks5 错误样本, 来自生产日志: ollama 走 socks5 代理时偶发的代理节点不可用。
var errSocksProxy = fmt.Errorf("post \"https://ollama.com/v1/chat/completions\": socks connect tcp resin:2260->ollama.com:443: unknown error general SOCKS server failure")

func TestIsInfrastructureError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"socks5 代理失败", errSocksProxy, true},
		{"connection refused", errors.New("dial tcp 127.0.0.1:443: connect: connection refused"), true},
		{"connection reset", errors.New("read tcp 1.2.3.4:443: read: connection reset by peer"), true},
		{"DNS 解析失败", errors.New("dial tcp: lookup api.example.com: no such host"), true},
		{"TLS handshake", errors.New("net/http: TLS handshake error"), true},
		{"x509 证书", errors.New("x509: certificate signed by unknown authority"), true},
		{"EOF", errors.New("unexpected EOF"), true},
		// 真实生产样本: 非流式透传 Do 阶段连接提前中断(url.Error 包装 io.ErrUnexpectedEOF),
		// 上游未返回任何 HTTP 响应, 属基础设施错误, 应按网络错误重试策略处理。
		{"url.Error unexpected EOF", &url.Error{Op: "Post", URL: "https://api.stepfun.com/step_plan/v1/chat/completions", Err: io.ErrUnexpectedEOF}, true},
		{"broken pipe", errors.New("write tcp: broken pipe"), true},
		{"net.OpError", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("test")}, true},
		// httpclient 业务错误(有 StatusCode) 不算基础设施
		{"http 401", &httpclient.Error{Method: "POST", URL: "u", StatusCode: 401, Status: "401 Unauthorized"}, false},
		{"http 500", &httpclient.Error{Method: "POST", URL: "u", StatusCode: 500, Status: "500 Internal Server Error"}, false},
		{"http 429", &httpclient.Error{Method: "POST", URL: "u", StatusCode: 429, Status: "429 Too Many Requests"}, false},
		// httpclient 网络错误(无 StatusCode) 算基础设施: 表示连接阶段就失败了, 没有 HTTP 响应
		{"httpclient 无 status", &httpclient.Error{Method: "POST", URL: "u", StatusCode: 0, Status: "socks connect tcp ... general SOCKS server failure"}, true},
		// 业务错误 "Request failed: Bad Request" 不算
		{"Request failed: Bad Request", errors.New("Request failed: Bad Request, ..."), false},
		{"Request failed: Service Unavailable", errors.New("Request failed: Service Unavailable, ..."), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isInfrastructureError(c.err); got != c.want {
				t.Errorf("isInfrastructureError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestClassifyRoundInfraError 验证 classifyRound 把网络层错误归到 ErrClassUpstreamNetwork 而非 ErrClassUpstream。
func TestClassifyRoundInfraError(t *testing.T) {
	got := classifyRound(errSocksProxy, nil, nil)
	if got != ErrClassUpstreamNetwork {
		t.Fatalf("classifyRound(errSocksProxy) = %q, want %q", got, ErrClassUpstreamNetwork)
	}
}

// TestClassifyRoundBusinessError 验证业务错误仍走 status code 归类路径, 不被误判为网络错误。
func TestClassifyRoundBusinessError(t *testing.T) {
	err := errors.New("Request failed: Bad Request, ...")
	got := classifyRound(err, nil, nil)
	if got != ErrClassUpstream4xx {
		t.Fatalf("classifyRound(bad request) = %q, want %q", got, ErrClassUpstream4xx)
	}
}

// TestClassifyRoundHTTP401NotInfra 验证 401 是业务错误, 不是基础设施错误。
func TestClassifyRoundHTTP401NotInfra(t *testing.T) {
	err := &httpclient.Error{Method: "POST", URL: "u", StatusCode: 401, Status: "401 Unauthorized"}
	got := classifyRound(err, nil, nil)
	if got != ErrClassUpstream4xx {
		t.Fatalf("classifyRound(http 401) = %q, want %q", got, ErrClassUpstream4xx)
	}
}
