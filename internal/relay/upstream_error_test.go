package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestReadUpstreamErrorBodyIsBounded(t *testing.T) {
	input := bytes.Repeat([]byte("x"), errBodySnippetLimit*4)
	got, err := readUpstreamErrorBody(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("readUpstreamErrorBody: %v", err)
	}
	if len(got) <= errBodySnippetLimit {
		t.Fatalf("expected truncation marker, got %d bytes", len(got))
	}
	if !strings.HasSuffix(string(got), "...[truncated]") {
		t.Fatalf("missing truncation marker: %q", string(got[len(got)-32:]))
	}
	if len(got) > errBodySnippetLimit+len("...[truncated]") {
		t.Fatalf("body exceeds diagnostic budget: %d", len(got))
	}
}

func TestPassthroughStreamErrorPreservesRetryAfter(t *testing.T) {
	readFailure := io.ErrUnexpectedEOF
	closeFailure := errors.New("upstream error body close failed")
	for _, tc := range []struct {
		name     string
		body     string
		readErr  error
		closeErr error
	}{
		{name: "normal", body: `{"error":"rate limited"}`},
		{name: "partial", body: `{"error":"partial`, readErr: readFailure, closeErr: closeFailure},
		{name: "bounded", body: strings.Repeat("x", errBodySnippetLimit*4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &upstreamErrorTestBody{reader: strings.NewReader(tc.body), readErr: tc.readErr, closeErr: tc.closeErr}
			headers := http.Header{"Retry-After": {"900"}, "X-Request-Id": {"request-429"}}
			client := &http.Client{Transport: upstreamErrorTestTransport{body: body, headers: headers}}
			request := &httpclient.Request{Method: http.MethodPost, URL: "http://upstream.invalid/chat/completions", Body: []byte(`{"stream":true}`)}
			resp, err := sendPassthroughStream(context.Background(), llm.APIFormatOpenAIChatCompletion, request, client, "*/*")
			if resp != nil || err == nil {
				t.Fatalf("expected upstream failure, got resp=%v err=%v", resp, err)
			}
			var failure *httpclient.Error
			if !errors.As(err, &failure) {
				t.Fatalf("missing structured upstream error: %v", err)
			}
			if code, ok := UpstreamStatusCode(err); !ok || code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, %v", code, ok)
			}
			if isInfrastructureError(err) {
				t.Fatalf("HTTP 429 was misclassified as infrastructure failure: %v", err)
			}
			if got := UpstreamRetryAfter(err); got != "900" {
				t.Fatalf("Retry-After = %q", got)
			}
			if got := opencodeRejectCooldown(UpstreamRetryAfter(err), time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)); got != 15*time.Minute {
				t.Fatalf("cooldown = %s", got)
			}
			if failure.Method != request.Method || failure.URL != request.URL || failure.Status != "429 Too Many Requests" {
				t.Fatalf("lost upstream diagnostics: %#v", failure)
			}
			if len(tc.body) <= errBodySnippetLimit && string(failure.Body) != tc.body {
				t.Fatalf("partial body = %q, want %q", failure.Body, tc.body)
			}
			if len(failure.Body) > errBodySnippetLimit+len("...[truncated]") {
				t.Fatalf("error body exceeds budget: %d", len(failure.Body))
			}
			if tc.readErr != nil && !errors.Is(err, tc.readErr) {
				t.Fatalf("lost read error: %v", err)
			}
			if tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
				t.Fatalf("lost close error: %v", err)
			}
			if body.closed != 1 {
				t.Fatalf("body closed %d times", body.closed)
			}
			headers.Set("Retry-After", "1")
			if UpstreamRetryAfter(err) != "900" {
				t.Fatal("error headers share mutable response headers")
			}
		})
	}
}

type upstreamErrorTestBody struct {
	reader   *strings.Reader
	readErr  error
	closeErr error
	closed   int
}

func (b *upstreamErrorTestBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF && b.readErr != nil {
		return n, b.readErr
	}
	return n, err
}

func (b *upstreamErrorTestBody) Close() error {
	b.closed++
	return b.closeErr
}

type upstreamErrorTestTransport struct {
	body    io.ReadCloser
	headers http.Header
}

func (t upstreamErrorTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: t.headers, Body: t.body, Request: request}, nil
}
