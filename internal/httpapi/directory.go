package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/service"
)

// DashboardService serves the overview and the notification feed.
type DashboardService interface {
	Overview(ctx context.Context) (service.Overview, error)
	Notifications(ctx context.Context, actor auth.Claims, limit int) ([]service.Notification, error)
}

// Features tells the UI which optional integrations are live, and in which
// mode, so it can label simulated ones instead of hiding them.
type Features struct {
	Billing string `json:"billing"` // "stripe", "fake" or "disabled"
	Agent   string `json:"agent"`   // "openai" or "fake"
}

// me returns the signed-in user and the enabled features.
func (h *handlers) me(c *gin.Context) {
	claims, _ := claimsFrom(c)
	u, err := h.users.Get(c.Request.Context(), claims.UserID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": u, "features": h.features})
}

// listUsers serves the people directory. With ?ids=1,2,3 it returns exactly
// those users (one request for every person on a screen); otherwise a
// searchable page (?q=, limit, offset).
func (h *handlers) listUsers(c *gin.Context) {
	if raw := c.Query("ids"); raw != "" {
		var ids []int64
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id < 1 {
				abortWithError(c, http.StatusBadRequest, "invalid_input", "ids must be a comma-separated list of positive integers")
				return
			}
			ids = append(ids, id)
		}
		users, err := h.users.GetMany(c.Request.Context(), ids)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": users})
		return
	}

	limit, ok := queryInt(c, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(c, "offset")
	if !ok {
		return
	}
	page, err := h.users.List(c.Request.Context(), c.Query("q"), limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

type dashboardHandlers struct {
	dashboard DashboardService
}

func (h *dashboardHandlers) overview(c *gin.Context) {
	o, err := h.dashboard.Overview(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, o)
}

func (h *dashboardHandlers) notifications(c *gin.Context) {
	limit, ok := queryInt(c, "limit")
	if !ok {
		return
	}
	claims, _ := claimsFrom(c)
	items, err := h.dashboard.Notifications(c.Request.Context(), claims, limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
