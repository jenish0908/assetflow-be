package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow/internal/auth"
)

const (
	requestIDHeader = "X-Request-ID"
	keyRequestID    = "request_id"
	keyClaims       = "auth_claims"
)

// requestID reuses a well-formed X-Request-ID from the caller (so one ID can
// follow a request across services) or generates a new one, and echoes it
// in the response.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Set(keyRequestID, id)
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}

// validRequestID accepts short IDs of letters, digits, '-' and '_' only, so a
// caller cannot inject arbitrary text into our logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func requestIDFrom(c *gin.Context) string {
	return c.GetString(keyRequestID)
}

// requestLogger writes one structured log line per request after it finishes.
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		route := c.FullPath() // the route pattern, e.g. /assets/:id, keeps log cardinality low
		if route == "" {
			route = "unmatched"
		}
		attrs := []slog.Attr{
			slog.String("request_id", requestIDFrom(c)),
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.String("client_ip", c.ClientIP()),
		}
		if claims, ok := claimsFrom(c); ok {
			attrs = append(attrs, slog.Int64("user_id", claims.UserID))
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("err", c.Errors.String()))
		}

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		logger.LogAttrs(c.Request.Context(), level, "http request", attrs...)
	}
}

// recovery turns a panic in a handler into a JSON 500 instead of a dropped
// connection, and logs the stack trace.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if err, ok := r.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(r) // deliberate abort; let net/http handle it
			}
			logger.ErrorContext(c.Request.Context(), "panic recovered",
				"request_id", requestIDFrom(c),
				"panic", r,
				"stack", string(debug.Stack()),
			)
			abortWithError(c, http.StatusInternalServerError, "internal", "internal server error")
		}()
		c.Next()
	}
}

// TokenParser verifies access tokens. *auth.TokenManager satisfies it.
type TokenParser interface {
	Parse(token string) (auth.Claims, error)
}

// authenticate requires a valid "Authorization: Bearer <jwt>" header and
// stores the verified claims on the context.
func authenticate(tokens TokenParser) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !ok || token == "" {
			abortWithError(c, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
			return
		}
		claims, err := tokens.Parse(token)
		if err != nil {
			abortWithError(c, http.StatusUnauthorized, "unauthenticated", "invalid or expired token")
			return
		}
		c.Set(keyClaims, claims)
		c.Next()
	}
}

// requireRole allows the request only if the caller's role is at least
// required. It must run after authenticate.
func requireRole(required auth.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := claimsFrom(c)
		if !ok || !claims.Role.AtLeast(required) {
			abortWithError(c, http.StatusForbidden, "forbidden", "requires role "+string(required)+" or higher")
			return
		}
		c.Next()
	}
}

func claimsFrom(c *gin.Context) (auth.Claims, bool) {
	v, ok := c.Get(keyClaims)
	if !ok {
		return auth.Claims{}, false
	}
	claims, ok := v.(auth.Claims)
	return claims, ok
}
