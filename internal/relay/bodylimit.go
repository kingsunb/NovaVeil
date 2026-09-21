package relay

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/looplj/axonhub/llm/httpclient"
)

const (
	maxRelayCompressedBodyBytes   int64 = 16 * 1024 * 1024
	maxRelayDecompressedBodyBytes int64 = 64 * 1024 * 1024
	// defaultRelayBodyBudgetBytes 是进程内请求体总预算。
	// 单请求解压上限 64MiB, 128MiB 同时只放得下两个满载正文, 用来挡住并发大包;
	// 普通几十字节到几 MiB 的请求按实际占用扣减, 预算有余量时立即放行, 不会排队。
	// 读入的压缩字节与解压产出的字节在重叠期间一起计入, 压缩缓冲丢弃后只留解压占用。
	defaultRelayBodyBudgetBytes int64 = 128 * 1024 * 1024
)

var ErrRelayBodyTooLarge = errors.New("request body too large")

// relayBodyBudget 是可取消的进程级请求体字节预算。
// 超额拒绝与客户端取消都不占用额度; 额度用尽不是上游故障, 不进入成员冷却,
// 与渠道并发槽位满(channellimit)一样只拒绝本次准入。不另建指标系统。
var relayBodyBudget = newRelayBodyBudget(defaultRelayBodyBudgetBytes)

type relayBodyBudgetState struct {
	mu     sync.Mutex
	notify chan struct{}
	used   int64
	limit  int64
}

func newRelayBodyBudget(limit int64) *relayBodyBudgetState {
	return &relayBodyBudgetState{notify: make(chan struct{}), limit: limit}
}

// acquireRelayBodyBudget 预留 n 字节。n 大于整份预算时立即拒绝(永远排不到);
// 暂时不够则等待其他请求释放, ctx 取消时返回 ctx.Err() 且不预留。
func acquireRelayBodyBudget(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	relayBodyBudget.mu.Lock()
	if n > relayBodyBudget.limit {
		relayBodyBudget.mu.Unlock()
		return ErrRelayBodyTooLarge
	}
	for relayBodyBudget.used+n > relayBodyBudget.limit {
		notify := relayBodyBudget.notify
		relayBodyBudget.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relayBodyBudget.mu.Lock()
		if n > relayBodyBudget.limit {
			relayBodyBudget.mu.Unlock()
			return ErrRelayBodyTooLarge
		}
	}
	relayBodyBudget.used += n
	relayBodyBudget.mu.Unlock()
	return nil
}

// tryAcquireRelayBodyBudget 立即预留 n 字节。不够就拒绝, 不等待。
// 同一请求已经持有读入额度时, 再为字符串副本等待会把自己卡住。
func tryAcquireRelayBodyBudget(n int64) error {
	if n <= 0 {
		return nil
	}
	relayBodyBudget.mu.Lock()
	defer relayBodyBudget.mu.Unlock()
	if relayBodyBudget.used+n > relayBodyBudget.limit {
		return ErrRelayBodyTooLarge
	}
	relayBodyBudget.used += n
	return nil
}

// releaseRelayBodyBudget 归还 n 字节并唤醒等待者。n<=0 或超额归还都是 no-op 到不小于 0。
func releaseRelayBodyBudget(n int64) {
	if n <= 0 {
		return
	}
	relayBodyBudget.mu.Lock()
	relayBodyBudget.used -= n
	if relayBodyBudget.used < 0 {
		relayBodyBudget.used = 0
	}
	close(relayBodyBudget.notify)
	relayBodyBudget.notify = make(chan struct{})
	relayBodyBudget.mu.Unlock()
}

// relayBodyBudgetInUse 返回当前已预留的请求体字节数, 供测试核对释放。
func relayBodyBudgetInUse() int64 {
	relayBodyBudget.mu.Lock()
	defer relayBodyBudget.mu.Unlock()
	return relayBodyBudget.used
}

// testingSetRelayBodyBudget 临时替换进程预算上限, 返回恢复函数。
func testingSetRelayBodyBudget(limit int64) func() {
	relayBodyBudget.mu.Lock()
	old := relayBodyBudget.limit
	relayBodyBudget.limit = limit
	relayBodyBudget.mu.Unlock()
	return func() {
		relayBodyBudget.mu.Lock()
		relayBodyBudget.limit = old
		close(relayBodyBudget.notify)
		relayBodyBudget.notify = make(chan struct{})
		relayBodyBudget.mu.Unlock()
	}
}

// readLimitedHTTPRequest 读取并按需解压客户端请求体。
// 成功时 release 归还本请求仍占用的预算(解压后的正文); 调用方必须在全部结束路径调用它。
// 失败时已读字节的预算在返回前释放, release 为 nil。
func readLimitedHTTPRequest(rawReq *http.Request) (*httpclient.Request, func(), error) {
	req := &httpclient.Request{
		Method:     rawReq.Method,
		URL:        rawReq.URL.String(),
		Path:       rawReq.URL.Path,
		Query:      rawReq.URL.Query(),
		Headers:    rawReq.Header.Clone(),
		Auth:       &httpclient.AuthConfig{},
		ClientIP:   clientIP(rawReq),
		RawRequest: rawReq,
	}

	ctx := rawReq.Context()
	body, compressedCharge, err := readBudget(ctx, rawReq.Body, maxRelayCompressedBodyBytes)
	if err != nil {
		return nil, nil, err
	}
	if len(body) == 0 {
		releaseRelayBodyBudget(compressedCharge)
		return req, func() {}, nil
	}
	decoded, extraCharge, separate, err := decodeLimitedBodyBudget(ctx, body, req.Headers, maxRelayDecompressedBodyBytes)
	if err != nil {
		releaseRelayBodyBudget(compressedCharge)
		releaseRelayBodyBudget(extraCharge)
		return nil, nil, err
	}
	held := compressedCharge
	if separate {
		// 解压缓冲与压缩缓冲同时存活到这里; 压缩缓冲随后丢弃, 只留解压占用。
		releaseRelayBodyBudget(compressedCharge)
		held = extraCharge
	}
	req.Body = decoded
	var once sync.Once
	return req, func() { once.Do(func() { releaseRelayBodyBudget(held) }) }, nil
}

func decodeLimitedBody(body []byte, headers http.Header, limit int64) ([]byte, error) {
	decoded, _, _, err := decodeLimitedBodyBudget(context.Background(), body, headers, limit)
	return decoded, err
}

// decodeLimitedBodyBudget 解压 body。separate 为 true 时 decoded 是独立缓冲,
// 调用方在丢弃压缩字节后只保留 extra 这笔解压占用; identity 时 separate 为 false,
// 读入占用继续代表正文。
func decodeLimitedBodyBudget(ctx context.Context, body []byte, headers http.Header, limit int64) ([]byte, int64, bool, error) {
	encoding := strings.ToLower(strings.TrimSpace(headers.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		if int64(len(body)) > limit {
			return nil, 0, false, ErrRelayBodyTooLarge
		}
		return body, 0, false, nil
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, 0, false, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer reader.Close()
		decoded, charge, err := readDecodedBudget(ctx, reader, headers, limit, "gzip")
		return decoded, charge, true, err
	case "deflate":
		decoded, charge, err := decodeLimitedDeflateBudget(ctx, body, limit)
		if err != nil {
			return nil, 0, true, fmt.Errorf("failed to decompress deflate body: %w", err)
		}
		headers.Del("Content-Encoding")
		headers.Del("Content-Length")
		return decoded, charge, true, nil
	case "zstd":
		decoder, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(uint64(limit)))
		if err != nil {
			return nil, 0, false, fmt.Errorf("failed to create zstd decoder: %w", err)
		}
		defer decoder.Close()
		decoded, charge, err := readDecodedBudget(ctx, decoder, headers, limit, "zstd")
		return decoded, charge, true, err
	default:
		return nil, 0, false, fmt.Errorf("unsupported content encoding: %s", headers.Get("Content-Encoding"))
	}
}

func decodeLimitedDeflate(body []byte, limit int64) ([]byte, error) {
	decoded, _, err := decodeLimitedDeflateBudget(context.Background(), body, limit)
	return decoded, err
}

func decodeLimitedDeflateBudget(ctx context.Context, body []byte, limit int64) ([]byte, int64, error) {
	if reader, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
		defer reader.Close()
		return readBudget(ctx, reader, limit)
	}
	reader := flate.NewReader(bytes.NewReader(body))
	defer reader.Close()
	return readBudget(ctx, reader, limit)
}

func readDecoded(reader io.Reader, headers http.Header, limit int64, encoding string) ([]byte, error) {
	decoded, _, err := readDecodedBudget(context.Background(), reader, headers, limit, encoding)
	return decoded, err
}

func readDecodedBudget(ctx context.Context, reader io.Reader, headers http.Header, limit int64, encoding string) ([]byte, int64, error) {
	decoded, charge, err := readBudget(ctx, reader, limit)
	if err != nil {
		if errors.Is(err, ErrRelayBodyTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("failed to decompress %s body: %w", encoding, err)
	}
	headers.Del("Content-Encoding")
	headers.Del("Content-Length")
	return decoded, charge, nil
}

func readLimit(reader io.Reader, limit int64) ([]byte, error) {
	data, _, err := readBudget(context.Background(), reader, limit)
	return data, err
}

// readBudget 按实际读到的字节预留预算, 并遵守单请求上限。
// 成功时 charge 仍被占用, 由调用方在缓冲丢弃或请求结束时释放。
// 任何失败(超限、取消、读错误)都在返回前释放已占用的额度。
func readBudget(ctx context.Context, reader io.Reader, limit int64) ([]byte, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil {
		return nil, 0, nil
	}
	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	var charged int64
	releaseCharged := func() {
		if charged > 0 {
			releaseRelayBodyBudget(charged)
			charged = 0
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			releaseCharged()
			return nil, 0, err
		}
		n, err := reader.Read(tmp)
		if n > 0 {
			if int64(buf.Len())+int64(n) > limit {
				releaseCharged()
				return nil, 0, ErrRelayBodyTooLarge
			}
			if aerr := acquireRelayBodyBudget(ctx, int64(n)); aerr != nil {
				releaseCharged()
				return nil, 0, aerr
			}
			charged += int64(n)
			buf.Write(tmp[:n])
		}
		if err == io.EOF {
			return buf.Bytes(), charged, nil
		}
		if err != nil {
			releaseCharged()
			return nil, 0, err
		}
	}
}

func clientIP(req *http.Request) string {
	if ip, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		return ip
	}
	return req.RemoteAddr
}
