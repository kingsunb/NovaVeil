package resp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
