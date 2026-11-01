package relay

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseLimitTransportFunc func(*http.Request) (*http.Response, error)

func (f responseLimitTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type trackingBody struct {
	reader io.Reader
	read   bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	b.read = true
	return b.reader.Read(p)
}

func (b *trackingBody) Close() error { return nil }

func responseWithBody(contentLength int64, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: contentLength,
		Body:          body,
		Header:        make(http.Header),
	}
}

func TestResponseBodyLimitAllowsExactLimit(t *testing.T) {
	client := withResponseBodyLimit(&http.Client{
		Transport: responseLimitTransportFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(4, io.NopCloser(strings.NewReader("1234"))), nil
		}),
	}, 4)

	resp, err := client.Get("http://upstream.invalid/")
	if err != nil {
		t.Fatalf("exact-limit response must succeed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read exact-limit body: %v", err)
	}
	if string(body) != "1234" {
		t.Fatalf("body = %q, want 1234", body)
	}
}

func TestResponseBodyLimitRejectsChunkedOverflow(t *testing.T) {
	client := withResponseBodyLimit(&http.Client{
		Transport: responseLimitTransportFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(-1, io.NopCloser(strings.NewReader("ABCDE"))), nil
		}),
	}, 4)

	resp, err := client.Get("http://upstream.invalid/")
	if err != nil {
		t.Fatalf("chunked response headers must succeed: %v", err)
	}
	defer resp.Body.Close()
	_, readErr := io.ReadAll(resp.Body)
	if !errors.Is(readErr, ErrUpstreamResponseTooLarge) {
		t.Fatalf("read err = %v, want ErrUpstreamResponseTooLarge", readErr)
	}
	if strings.Contains(readErr.Error(), "ABCDE") {
		t.Fatal("error must not include the upstream body")
	}
}

func TestResponseBodyLimitRejectsDeclaredLengthWithoutReading(t *testing.T) {
	body := &trackingBody{reader: strings.NewReader("ABCDE")}
	client := withResponseBodyLimit(&http.Client{
		Transport: responseLimitTransportFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(5, body), nil
		}),
	}, 4)

	_, err := client.Get("http://upstream.invalid/")
	if !errors.Is(err, ErrUpstreamResponseTooLarge) {
		t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
	}
	if body.read {
		t.Fatal("declared oversized body must be rejected before reading")
	}
}
