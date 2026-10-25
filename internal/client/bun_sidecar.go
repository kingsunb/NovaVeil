package client

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed bun_sidecar.js
var bunSidecarScript []byte

// bunForwardRequest 是 Go 传给 Bun sidecar 的本地转发载荷。
type bunForwardRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers [][2]string `json:"headers,omitempty"`
	Body    string      `json:"body,omitempty"`
	Proxy   string      `json:"proxy,omitempty"`
}

// bunSidecar 是一个常驻 Bun 进程, endpoint 指向其本地 /forward。
type bunSidecar struct {
	cmd      *exec.Cmd
	endpoint string
	done     chan struct{}
}

// bunSidecarManager 管理全局 sidecar 并在进程退出后按需重启。
type bunSidecarManager struct {
	mu       sync.Mutex
	sidecar  *bunSidecar
	startErr error
}

var bunSidecars = &bunSidecarManager{}

var bunSidecarHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// bunSidecarCommand 解析显式配置的 Bun 可执行文件; 未配置时不启用 sidecar。
func bunSidecarCommand() (path string, explicit bool, err error) {
	for _, key := range []string{"NOVEIL_BUN_BIN", "BUN_BIN"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		found, lookupErr := exec.LookPath(value)
		if lookupErr != nil {
			return "", true, fmt.Errorf("env %s bun runtime: %w", key, lookupErr)
		}
		return found, true, nil
	}
	return "", false, nil
}

// current 返回存活 sidecar, 进程退出后重启; 启动失败只报告不伪造成功。
func (m *bunSidecarManager) current() (*bunSidecar, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sidecar != nil && m.sidecar.alive() {
		return m.sidecar, nil
	}
	if m.startErr != nil {
		return nil, m.startErr
	}
	binary, _, err := bunSidecarCommand()
	if err != nil {
		m.startErr = err
		return nil, err
	}
	if binary == "" {
		m.startErr = errors.New("bun runtime not found")
		return nil, m.startErr
	}
	sidecar, err := startBunSidecar(binary)
	if err != nil {
		m.startErr = err
		return nil, err
	}
	m.sidecar = sidecar
	return sidecar, nil
}

// startBunSidecar 启动 sidecar 并等待其回传监听端口。
func startBunSidecar(binary string) (*bunSidecar, error) {
	script, err := os.CreateTemp("", "novaveil-bun-sidecar-*.js")
	if err != nil {
		return nil, fmt.Errorf("create bun sidecar script: %w", err)
	}
	scriptPath := script.Name()
	if _, err := script.Write(bunSidecarScript); err != nil {
		script.Close()
		os.Remove(scriptPath)
		return nil, fmt.Errorf("write bun sidecar script: %w", err)
	}
	if err := script.Close(); err != nil {
		os.Remove(scriptPath)
		return nil, fmt.Errorf("close bun sidecar script: %w", err)
	}

	cmd := exec.Command(binary, "run", scriptPath)
	cmd.Env = append(os.Environ(), "NOVEIL_PARENT_PID="+strconv.Itoa(os.Getpid()))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.Remove(scriptPath)
		return nil, fmt.Errorf("bun sidecar stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.Remove(scriptPath)
		return nil, fmt.Errorf("bun sidecar stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		return nil, fmt.Errorf("start bun sidecar: %w", err)
	}

	type ready struct {
		line string
		err  error
	}
	readyCh := make(chan ready, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		readyCh <- ready{line: line, err: readErr}
	}()
	select {
	case result := <-readyCh:
		if result.err != nil {
			go func() {
				_ = cmd.Wait()
				os.Remove(scriptPath)
			}()
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("bun sidecar ready: %w", result.err)
		}
		var status struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(result.line)), &status); err != nil || status.Port <= 0 {
			go func() {
				_ = cmd.Wait()
				os.Remove(scriptPath)
			}()
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("bun sidecar ready payload %q", strings.TrimSpace(result.line))
		}
		sidecar := &bunSidecar{
			cmd:      cmd,
			endpoint: "http://127.0.0.1:" + strconv.Itoa(status.Port) + "/forward",
			done:     make(chan struct{}),
		}
		var pipes sync.WaitGroup
		pipes.Add(2)
		go func() {
			defer pipes.Done()
			_, _ = io.Copy(io.Discard, stdout)
		}()
		go func() {
			defer pipes.Done()
			_, _ = io.Copy(io.Discard, stderr)
		}()
		go func() {
			pipes.Wait()
			defer close(sidecar.done)
			_ = cmd.Wait()
			os.Remove(scriptPath)
		}()
		return sidecar, nil
	case <-time.After(5 * time.Second):
		go func() {
			_ = cmd.Wait()
			os.Remove(scriptPath)
		}()
		_ = cmd.Process.Kill()
		return nil, errors.New("bun sidecar startup timeout")
	}
}

func (s *bunSidecar) alive() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *bunSidecar) Close() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if err := s.cmd.Process.Kill(); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("bun sidecar close timeout")
	}
}

// bunSidecarTransport 把 http.RoundTripper 请求交给常驻 Bun 进程发送。
type bunSidecarTransport struct {
	manager *bunSidecarManager
	proxy   *url.URL
	client  *http.Client
}

// newBunSidecarClient 在 Bun 可用时返回真 BoringSSL 传输; 不可用时返回 nil 回退 uTLS。
func newBunSidecarClient(proxyURL *url.URL) (*bunSidecarTransport, error) {
	if proxyURL != nil {
		switch proxyURL.Scheme {
		case "http", "https":
		default:
			return nil, fmt.Errorf("bun sidecar unsupported proxy scheme %q", proxyURL.Scheme)
		}
	}
	binary, _, err := bunSidecarCommand()
	if err != nil {
		return nil, err
	}
	if binary == "" {
		return nil, nil
	}
	if _, err := bunSidecars.current(); err != nil {
		return nil, err
	}
	return &bunSidecarTransport{
		manager: bunSidecars,
		proxy:   proxyURL,
		client:  bunSidecarHTTPClient,
	}, nil
}

// RoundTrip 序列化请求并流式回传 Bun fetch 的上游响应。
func (t *bunSidecarTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}
	sidecar, err := t.manager.current()
	if err != nil {
		return nil, err
	}
	payload := bunForwardRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: bunSidecarHeaders(req),
		Proxy:   "",
	}
	if body != nil {
		payload.Body = base64.StdEncoding.EncodeToString(body)
	}
	if t.proxy != nil {
		payload.Proxy = t.proxy.String()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("bun sidecar payload: %w", err)
	}
	forward, err := http.NewRequestWithContext(req.Context(), http.MethodPost, sidecar.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("bun sidecar request: %w", err)
	}
	forward.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(forward)
	if err != nil {
		return nil, bunContextError(req.Context(), err)
	}
	if response.Header.Get("X-Noveil-Sidecar-Error") == "1" {
		message := strings.TrimSpace(bunSidecarErrorMessage(response))
		if message == "" {
			message = response.Status
		}
		return nil, fmt.Errorf("bun sidecar: %s", message)
	}
	return &http.Response{
		Status:        response.Status,
		StatusCode:    response.StatusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        response.Header.Clone(),
		Body:          response.Body,
		ContentLength: response.ContentLength,
		Request:       req,
	}, nil
}

func (t *bunSidecarTransport) CloseIdleConnections() {
	t.client.CloseIdleConnections()
}

func bunSidecarErrorMessage(response *http.Response) string {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024))
	if err != nil {
		return ""
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return string(data)
}

// bunSidecarHeaders 保留公共头官方拼写, 其余头统一小写。
func bunSidecarHeaders(req *http.Request) [][2]string {
	common := make(map[string]string, len(bunCommonHeaders))
	for _, name := range bunCommonHeaders {
		common[strings.ToLower(name)] = name
	}
	var out [][2]string
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values := req.Header[name]
		lower := strings.ToLower(name)
		switch lower {
		case "host", "content-length", "connection", "transfer-encoding":
			continue
		}
		wire := lower
		if canonical, ok := common[lower]; ok {
			wire = canonical
		}
		for _, value := range values {
			out = append(out, [2]string{wire, value})
		}
	}
	return out
}

// ensure bun sidecar transports implement http.RoundTripper.
var _ http.RoundTripper = (*bunSidecarTransport)(nil)
