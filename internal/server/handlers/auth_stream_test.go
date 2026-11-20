package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/eval"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type authStreamProbe struct {
	path        string
	recorder    *httptest.ResponseRecorder
	entered     chan context.Context
	flushed     chan struct{}
	release     chan struct{}
	done        chan struct{}
	cancel      context.CancelFunc
	releaseOnce sync.Once
}

func (p *authStreamProbe) releaseFlush() {
	p.releaseOnce.Do(func() { close(p.release) })
}

type authStreamWriter struct {
	gin.ResponseWriter
	probe       *authStreamProbe
	request     func() context.Context
	enteredOnce sync.Once
	flushedOnce sync.Once
}

func (w *authStreamWriter) Header() http.Header {
	w.enteredOnce.Do(func() { w.probe.entered <- w.request() })
	return w.ResponseWriter.Header()
}

func (w *authStreamWriter) Flush() {
	w.ResponseWriter.Flush()
	w.flushedOnce.Do(func() {
		close(w.probe.flushed)
		<-w.probe.release
	})
}

func TestManagementStreamsEndWithSession(t *testing.T) {
	oldScheduler := eval.Default
	eval.Default = eval.NewScheduler()
	t.Cleanup(func() { eval.Default = oldScheduler })
	oldRateLimit := conf.AppConfig.Security.AdminAPIRateLimitEnabled
	conf.AppConfig.Security.AdminAPIRateLimitEnabled = false
	t.Cleanup(func() { conf.AppConfig.Security.AdminAPIRateLimitEnabled = oldRateLimit })

	const queuePath = "/api/v1/model-eval/queue/stream"
	paths := []string{"/api/v1/log/overview/stream", "/api/v1/group/runtime/stream", queuePath}
	for _, reason := range []string{"logout", "password_change", "expiration", "client_disconnect"} {
		t.Run(reason, func(t *testing.T) {
			seedAdminUser(t, "old-password-123")
			streams := make(map[string]*authStreamProbe, len(paths))
			for _, path := range paths {
				streams[path] = &authStreamProbe{
					path: path, recorder: httptest.NewRecorder(),
					entered: make(chan context.Context, 1), flushed: make(chan struct{}),
					release: make(chan struct{}), done: make(chan struct{}),
				}
			}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Writer = &authStreamWriter{
					ResponseWriter: c.Writer, probe: streams[c.Request.URL.Path],
					request: func() context.Context { return c.Request.Context() },
				}
				c.Next()
			})
			router.ResetRoutes()
			t.Cleanup(router.ResetRoutes)
			require.NoError(t, router.RegisterAll(r))

			secret, err := op.AuthJWTSecretGet()
			require.NoError(t, err)
			expires := time.Now().Add(time.Hour)
			if reason == "expiration" {
				expires = time.Now().Add(5 * time.Second)
			}
			claims := jwt.RegisteredClaims{
				Issuer: conf.APP_NAME, Audience: jwt.ClaimStrings{conf.APP_NAME},
				ExpiresAt: jwt.NewNumericDate(expires),
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
			require.NoError(t, err)

			for _, path := range paths {
				stream := streams[path]
				req := httptest.NewRequest(http.MethodGet, path, nil)
				requestContext, cancel := context.WithCancel(req.Context())
				stream.cancel = cancel
				req = req.WithContext(requestContext)
				req.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: token})
				go func() {
					defer close(stream.done)
					r.ServeHTTP(stream.recorder, req)
				}()
				t.Cleanup(func() {
					stream.cancel()
					stream.releaseFlush()
					select {
					case <-stream.done:
					case <-time.After(10 * time.Second):
						t.Errorf("stream did not stop during cleanup: %s", stream.path)
					}
				})
			}

			contexts := make(map[string]context.Context, len(paths))
			for _, path := range paths {
				select {
				case ctx := <-streams[path].entered:
					deadline, ok := ctx.Deadline()
					require.True(t, ok, "stream route must bind JWT expiry: %s", path)
					require.True(t, claims.ExpiresAt.Time.Equal(deadline), path)
					require.NoError(t, ctx.Err(), path)
					contexts[path] = ctx
				case <-time.After(10 * time.Second):
					t.Fatalf("stream did not enter its handler: %s", path)
				}
			}
			select {
			case <-streams[queuePath].flushed:
			case <-time.After(10 * time.Second):
				t.Fatal("queue stream did not flush its initial snapshot")
			}
			for _, stream := range streams {
				select {
				case <-stream.done:
					t.Fatalf("stream ended before session termination: %s", stream.path)
				default:
				}
			}

			switch reason {
			case "logout":
				require.NoError(t, op.AuthJWTRotateSecret())
			case "password_change":
				require.NoError(t, op.UserChangePassword("old-password-123", "new-password-456"))
			case "client_disconnect":
				for _, stream := range streams {
					stream.cancel()
				}
			}
			wantErr := context.Canceled
			if reason == "expiration" {
				wantErr = context.DeadlineExceeded
			}
			for _, path := range paths {
				ctx := contexts[path]
				select {
				case <-ctx.Done():
					assert.ErrorIs(t, ctx.Err(), wantErr, path)
				case <-time.After(10 * time.Second):
					t.Fatalf("session ended but stream context remained active: %s", path)
				}
			}

			eval.Publish()
			for _, stream := range streams {
				stream.releaseFlush()
			}
			for _, path := range paths {
				stream := streams[path]
				select {
				case <-stream.done:
				case <-time.After(10 * time.Second):
					t.Fatalf("stream handler did not exit: %s", path)
				}
				assert.Equal(t, http.StatusOK, stream.recorder.Code, path)
				assert.Equal(t, "text/event-stream", stream.recorder.Header().Get("Content-Type"), path)
				assert.NotContains(t, stream.recorder.Body.String(), ": ping", path)
			}
			assert.Equal(t, 1, strings.Count(streams[queuePath].recorder.Body.String(), "event:queue\n"))
		})
	}
}
