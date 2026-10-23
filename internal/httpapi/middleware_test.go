package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

type fakeTokenParser map[string]auth.Claims

func (f fakeTokenParser) Parse(token string) (auth.Claims, error) {
	c, ok := f[token]
	if !ok {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	return c, nil
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorDetail {
	t.Helper()
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Error
}

func TestAuthAndRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tokens := fakeTokenParser{
		"viewer-token":  {UserID: 1, Role: auth.RoleViewer},
		"manager-token": {UserID: 2, Role: auth.RoleManager},
		"admin-token":   {UserID: 3, Role: auth.RoleAdmin},
	}

	r := gin.New()
	r.Use(requestID())
	r.POST("/managers-only", authenticate(tokens), requireRole(auth.RoleManager), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name     string
		header   string
		wantCode int
		wantErr  string
	}{
		{"no header", "", http.StatusUnauthorized, "unauthenticated"},
		{"not bearer", "Basic abc", http.StatusUnauthorized, "unauthenticated"},
		{"bad token", "Bearer nope", http.StatusUnauthorized, "unauthenticated"},
		{"viewer is forbidden", "Bearer viewer-token", http.StatusForbidden, "forbidden"},
		{"manager allowed", "Bearer manager-token", http.StatusNoContent, ""},
		{"admin allowed", "Bearer admin-token", http.StatusNoContent, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/managers-only", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			r.ServeHTTP(rec, req)

			require.Equal(t, tt.wantCode, rec.Code)
			if tt.wantErr != "" {
				detail := decodeError(t, rec)
				assert.Equal(t, tt.wantErr, detail.Code)
				assert.NotEmpty(t, detail.RequestID, "errors should carry the request ID")
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestID())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, requestIDFrom(c)) })

	tests := []struct {
		name     string
		incoming string
		wantSame bool
	}{
		{"generated when missing", "", false},
		{"reused when valid", "abc-123_XYZ", true},
		{"replaced when unsafe", "bad id\ninjected", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.incoming != "" {
				req.Header.Set(requestIDHeader, tt.incoming)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			got := rec.Header().Get(requestIDHeader)
			assert.Equal(t, got, rec.Body.String(), "header and context must agree")
			if tt.wantSame {
				assert.Equal(t, tt.incoming, got)
			} else {
				assert.Len(t, got, 32)
			}
		})
	}
}

func TestRecoveryReturnsJSON500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestID(), recovery(discardLogger()))
	r.GET("/boom", func(*gin.Context) { panic(errors.New("boom")) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal", decodeError(t, rec).Code)
}

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newRateLimiter(1, 3, func() time.Time { return now })

	for i := range 3 {
		assert.True(t, l.allow("1.2.3.4"), "request %d within burst", i+1)
	}
	assert.False(t, l.allow("1.2.3.4"), "burst exhausted")
	assert.True(t, l.allow("5.6.7.8"), "other clients have their own bucket")

	now = now.Add(time.Second)
	assert.True(t, l.allow("1.2.3.4"), "one token refilled after 1s at 1 rps")

	now = now.Add(limiterIdleTTL + limiterSweepEvery + time.Second)
	l.allow("9.9.9.9") // triggers the sweep
	assert.Len(t, l.clients, 1, "idle clients are evicted")
}

func TestRateLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rateLimit(newRateLimiter(1, 1, time.Now)))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	codes := make([]int, 2)
	for i := range codes {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		codes[i] = rec.Code
	}
	assert.Equal(t, []int{http.StatusOK, http.StatusTooManyRequests}, codes)
}
