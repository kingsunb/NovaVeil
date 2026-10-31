package resp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestErrorSanitizesInternalMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	Error(c, http.StatusInternalServerError, "pq: password authentication failed for user dbadmin")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", w.Code)
	}
	var body ResponseStruct
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != ErrInternalServer {
		t.Fatalf("message = %q, want generic", body.Message)
	}
}

func TestErrorKeepsClientMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	Error(c, http.StatusBadRequest, "missing key")
	var body ResponseStruct
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != "missing key" {
		t.Fatalf("message = %q", body.Message)
	}
}

func TestErrorExposedKeepsInternalMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ErrorExposed(c, http.StatusInternalServerError, "upstream returned HTTP 401: invalid api key")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", w.Code)
	}
	var body ResponseStruct
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != "upstream returned HTTP 401: invalid api key" {
		t.Fatalf("message = %q, want raw upstream detail", body.Message)
	}
}

// TestGenRequestIDShape 验证请求 ID 形状: 时间戳前缀 + 合法字符集随机后缀, 且互不重复。
func TestGenRequestIDShape(t *testing.T) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := GenRequestID()
		if len(id) != 34 {
			t.Fatalf("id = %q, len = %d, want 34", id, len(id))
		}
		for _, r := range id[14:] {
			if !strings.ContainsRune(chars, r) {
				t.Fatalf("id %q 含非法后缀字符 %q", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("id %q 重复", id)
		}
		seen[id] = true
	}
}
