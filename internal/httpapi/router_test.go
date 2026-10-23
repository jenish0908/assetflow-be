package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		path     string
		pingErr  error
		wantCode int
		wantBody string
	}{
		{"healthz ok", "/healthz", nil, http.StatusOK, `{"status":"ok"}`},
		{"healthz ignores db", "/healthz", errors.New("down"), http.StatusOK, `{"status":"ok"}`},
		{"readyz ok", "/readyz", nil, http.StatusOK, `{"status":"ready"}`},
		{"readyz db down", "/readyz", errors.New("down"), http.StatusServiceUnavailable, `{"status":"unavailable"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRouter(Deps{
				Logger:    discardLogger(),
				DB:        fakePinger{err: tt.pingErr},
				RateLimit: RateLimit{RPS: 100, Burst: 100},
			})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)

			r.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
		})
	}
}
