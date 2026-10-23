// Package httpapi is the HTTP transport layer: routing, handlers and
// middleware. It is named httpapi rather than http to avoid shadowing net/http.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

// Pinger reports whether a dependency is reachable. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// RateLimit configures the per-client-IP token bucket.
type RateLimit struct {
	RPS   float64
	Burst int
}

// Deps are everything the router needs; main wires the concrete types.
type Deps struct {
	Logger *slog.Logger
	DB     Pinger
	Tokens TokenParser
	Users  UserService
	Assets AssetService
	// Billing is optional; nil disables the payment and webhook routes.
	Billing BillingService
	// Agent is optional; nil disables POST /agent/chat.
	Agent AgentService
	// Dashboard is optional; nil disables GET /stats and GET /notifications.
	Dashboard DashboardService
	Features  Features
	RateLimit RateLimit
}

// NewRouter builds the Gin engine with all middleware and routes registered.
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	// Trust no proxy headers: ClientIP is the TCP peer address, so clients
	// cannot dodge the rate limiter by sending a fake X-Forwarded-For.
	_ = r.SetTrustedProxies(nil)

	// Order matters: request ID first so everything after can log it; the
	// logger wraps recovery so a recovered panic is logged as a 500.
	r.Use(requestID(), requestLogger(d.Logger), recovery(d.Logger))
	r.NoRoute(func(c *gin.Context) {
		abortWithError(c, http.StatusNotFound, "not_found", "route not found")
	})

	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(d.Logger, d.DB))

	h := &handlers{users: d.Users, assets: d.Assets, features: d.Features}
	limiter := newRateLimiter(d.RateLimit.RPS, d.RateLimit.Burst, time.Now)

	api := r.Group("", rateLimit(limiter))
	api.POST("/auth/login", h.login)

	authed := api.Group("", authenticate(d.Tokens))
	viewer := requireRole(auth.RoleViewer)
	manager := requireRole(auth.RoleManager)
	admin := requireRole(auth.RoleAdmin)

	authed.GET("/me", viewer, h.me)
	authed.GET("/users", viewer, h.listUsers)
	authed.POST("/users", admin, h.createUser)

	authed.GET("/assets", viewer, h.listAssets)
	authed.GET("/assets/:id", viewer, h.getAsset)
	authed.POST("/assets", manager, h.createAsset)
	authed.PATCH("/assets/:id", manager, h.updateAsset)
	authed.DELETE("/assets/:id", admin, h.deleteAsset)
	authed.POST("/assets/:id/allocate", manager, h.allocateAsset)
	authed.POST("/assets/:id/return", manager, h.returnAsset)
	authed.PATCH("/assets/:id/status", manager, h.updateAssetStatus)

	if d.Billing != nil {
		bh := &billingHandlers{billing: d.Billing}
		// Stripe authenticates with a signature, not a JWT, and must not be
		// rate limited by IP.
		r.POST("/webhooks/stripe", bh.stripeWebhook)
		authed.POST("/payments", viewer, bh.createPayment)
		authed.GET("/payments", viewer, bh.listPayments)
		authed.GET("/payments/:id", viewer, bh.getPayment)
	}

	if d.Dashboard != nil {
		dh := &dashboardHandlers{dashboard: d.Dashboard}
		authed.GET("/stats", viewer, dh.overview)
		authed.GET("/notifications", viewer, dh.notifications)
	}

	if d.Agent != nil {
		// Any signed-in user may chat; each tool call is permission-checked
		// against the caller's role inside the agent.
		authed.POST("/agent/chat", viewer, (&agentHandlers{agent: d.Agent}).chat)
	}

	return r
}

// healthz is a liveness probe: the process is up and serving.
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readyz is a readiness probe: the process can reach its dependencies.
func readyz(logger *slog.Logger, db Pinger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "readiness check failed", "err", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
