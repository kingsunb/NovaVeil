package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func readBody(c *gin.Context, readErr *error, readN *int) {
	data, err := io.ReadAll(c.Request.Body)
	*readN, *readErr = len(data), err
	c.Status(http.StatusOK)
}

func serveBody(t *testing.T, r http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

func TestLoginBodyLimitBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var readErr error
	var readN int
	r.POST("/api/v1/user/login", LoginBodyLimit(), func(c *gin.Context) {
		readBody(c, &readErr, &readN)
	})

	exact := strings.Repeat("a", int(maxLoginBodyBytes))
	serveBody(t, r, "/api/v1/user/login", exact)
	if readErr != nil || readN != int(maxLoginBodyBytes) {
		t.Fatalf("exact limit body: n=%d err=%v, want n=%d err=nil", readN, readErr, maxLoginBodyBytes)
	}

	readErr, readN = nil, 0
	over := strings.Repeat("a", int(maxLoginBodyBytes)+1)
	serveBody(t, r, "/api/v1/user/login", over)
	var maxErr *http.MaxBytesError
	if !errors.As(readErr, &maxErr) {
		t.Fatalf("over limit body must fail with MaxBytesError, got err=%v", readErr)
	}
}

func TestLoginBodyLimitStacksUnderGlobalBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit())
	var readErr error
	var readN int
	r.POST("/api/v1/user/login", LoginBodyLimit(), func(c *gin.Context) {
		readBody(c, &readErr, &readN)
	})

	over := strings.Repeat("a", int(maxLoginBodyBytes)+1)
	serveBody(t, r, "/api/v1/user/login", over)
	var maxErr *http.MaxBytesError
	if !errors.As(readErr, &maxErr) {
		t.Fatalf("login limit must win over global 8MiB limit, got err=%v", readErr)
	}
}

func TestBodyLimitDoesNotApplyLoginCapToOtherAPIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit())
	var readErr error
	var readN int
	r.POST("/api/v1/channels", func(c *gin.Context) {
		readBody(c, &readErr, &readN)
	})

	body := strings.Repeat("a", int(maxLoginBodyBytes)+1)
	serveBody(t, r, "/api/v1/channels", body)
	if readErr != nil || readN != len(body) {
		t.Fatalf("other /api routes must keep 8MiB limit: n=%d err=%v", readN, readErr)
	}
}

func TestBodyLimitSkipsImportRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit())
	var readErr error
	var readN int
	r.POST("/api/v1/setting/import", func(c *gin.Context) {
		readBody(c, &readErr, &readN)
	})

	body := strings.Repeat("a", int(maxAPIBodyBytes)+1)
	serveBody(t, r, "/api/v1/setting/import", body)
	if readErr != nil || readN != len(body) {
		t.Fatalf("import route must bypass global 8MiB limit: n=%d err=%v", readN, readErr)
	}
}
