package httpapi

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/billing"
)

// BillingService is what the handlers need from billing.
type BillingService interface {
	CreatePayment(ctx context.Context, userID int64, idempotencyKey string, in billing.CreatePaymentInput) (billing.CreatePaymentResult, error)
	GetPayment(ctx context.Context, actor auth.Claims, id int64) (billing.Payment, error)
	HandleWebhook(ctx context.Context, payload []byte, signature string) error
	ListPayments(ctx context.Context, actor auth.Claims, limit, offset int) (billing.PaymentPage, error)
}

type billingHandlers struct {
	billing BillingService
}

// createPayment honours an optional Idempotency-Key header: resending the
// same request with the same key returns the original payment.
func (h *billingHandlers) createPayment(c *gin.Context) {
	claims, _ := claimsFrom(c) // route is behind authenticate
	var req billing.CreatePaymentInput
	if !bindJSON(c, &req) {
		return
	}
	res, err := h.billing.CreatePayment(c.Request.Context(), claims.UserID, c.GetHeader("Idempotency-Key"), req)
	if err != nil {
		respondError(c, err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		c.Header("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	c.JSON(status, res.Payment)
}

func (h *billingHandlers) getPayment(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	claims, _ := claimsFrom(c)
	p, err := h.billing.GetPayment(c.Request.Context(), claims, id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// stripeWebhook must see the exact bytes Stripe signed, so it reads the raw
// body instead of decoding JSON first. Any non-2xx makes Stripe retry.
func (h *billingHandlers) stripeWebhook(c *gin.Context) {
	payload, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	if err != nil {
		abortWithError(c, http.StatusBadRequest, "invalid_input", "could not read request body")
		return
	}
	if err := h.billing.HandleWebhook(c.Request.Context(), payload, c.GetHeader("Stripe-Signature")); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *billingHandlers) listPayments(c *gin.Context) {
	limit, ok := queryInt(c, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(c, "offset")
	if !ok {
		return
	}
	claims, _ := claimsFrom(c)
	page, err := h.billing.ListPayments(c.Request.Context(), claims, limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
