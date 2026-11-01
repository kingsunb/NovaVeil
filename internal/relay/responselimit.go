package relay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxRelayResponseBodyBytes 非流式响应体上限, 与请求解压一致。
const maxRelayResponseBodyBytes int64 = 64 * 1024 * 1024

// ErrUpstreamResponseTooLarge 表示上游非流式响应体超过进程可承受上限。
var ErrUpstreamResponseTooLarge = errors.New("upstream response body too large")

// withResponseBodyLimit 返回带上限客户端副本, 仅非流式使用。
func withResponseBodyLimit(client *http.Client, limit int64) *http.Client {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	limited := *client
	limited.Transport = &responseBodyLimitTransport{base: base, limit: limit}
	return &limited
}

type responseBodyLimitTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t *responseBodyLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > t.limit {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrUpstreamResponseTooLarge, resp.ContentLength, t.limit)
	}
	if resp.Body != nil {
		resp.Body = &responseBodyLimitReader{body: resp.Body, remaining: t.limit}
	}
	return resp, nil
}

// responseBodyLimitReader 到上限后多探一字节, 恰好等于上限放行。
type responseBodyLimitReader struct {
	body      io.ReadCloser
	remaining int64
}

func (r *responseBodyLimitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.body.Read(probe[:])
		if n > 0 {
			return 0, ErrUpstreamResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.body.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *responseBodyLimitReader) Close() error {
	return r.body.Close()
}
