package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/agent"
	"github.com/jenish0908/assetflow-be/internal/auth"
)

// AgentService answers natural-language requests on behalf of a caller.
type AgentService interface {
	Chat(ctx context.Context, actor auth.Claims, message string) (agent.Reply, error)
}

type agentHandlers struct {
	agent AgentService
}

func (h *agentHandlers) chat(c *gin.Context) {
	claims, _ := claimsFrom(c) // route is behind authenticate
	var req struct {
		Message string `json:"message"`
	}
	if !bindJSON(c, &req) {
		return
	}
	reply, err := h.agent.Chat(c.Request.Context(), claims, req.Message)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, reply)
}
