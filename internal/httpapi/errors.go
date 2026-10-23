package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/agent"
	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/service"
)

// Every error response has the same shape:
//
//	{"error": {"code": "not_found", "message": "asset 7 not found", "request_id": "..."}}
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func abortWithError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   msg,
		RequestID: requestIDFrom(c),
	}})
}

// respondError maps a service error to an HTTP status. Only *service.Error
// messages reach the client; anything unexpected becomes a generic 500 and
// the real error is attached to the context for the logging middleware.
func respondError(c *gin.Context, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, service.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_input"
	case errors.Is(err, service.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, service.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, service.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, billing.ErrGatewayUnavailable):
		status, code = http.StatusBadGateway, "payment_provider_unavailable"
	case errors.Is(err, agent.ErrLLMUnavailable):
		status, code = http.StatusBadGateway, "llm_unavailable"
	}

	msg := http.StatusText(status)
	var svcErr *service.Error
	if errors.As(err, &svcErr) {
		msg = svcErr.Msg
	}
	if status >= http.StatusInternalServerError {
		_ = c.Error(err)
	}
	abortWithError(c, status, code, msg)
}
