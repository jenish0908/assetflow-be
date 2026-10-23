package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow/internal/service"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// UserService is what the handlers need from the user service.
type UserService interface {
	Login(ctx context.Context, email, password string) (string, error)
	Create(ctx context.Context, in service.CreateUserInput) (service.User, error)
}

// AssetService is what the handlers need from the asset service.
type AssetService interface {
	Create(ctx context.Context, in service.NewAsset) (service.Asset, error)
	Get(ctx context.Context, id int64) (service.AssetWithAllocations, error)
	List(ctx context.Context, limit, offset int) (service.AssetPage, error)
	Update(ctx context.Context, id int64, p service.AssetPatch) (service.Asset, error)
	Delete(ctx context.Context, id int64) error
	Allocate(ctx context.Context, assetID, userID int64) (service.Allocation, error)
	Return(ctx context.Context, assetID int64) (service.Allocation, error)
	UpdateStatus(ctx context.Context, id int64, status service.AssetStatus) (service.Asset, error)
}

type handlers struct {
	users  UserService
	assets AssetService
}

func (h *handlers) login(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	token, err := h.users.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func (h *handlers) createUser(c *gin.Context) {
	var req service.CreateUserInput
	if !bindJSON(c, &req) {
		return
	}
	u, err := h.users.Create(c.Request.Context(), req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, u)
}

func (h *handlers) listAssets(c *gin.Context) {
	limit, ok := queryInt(c, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(c, "offset")
	if !ok {
		return
	}
	page, err := h.assets.List(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *handlers) createAsset(c *gin.Context) {
	var req service.NewAsset
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.assets.Create(c.Request.Context(), req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

func (h *handlers) getAsset(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	a, err := h.assets.Get(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *handlers) updateAsset(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req service.AssetPatch
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.assets.Update(c.Request.Context(), id, req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *handlers) deleteAsset(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.assets.Delete(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) allocateAsset(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	alloc, err := h.assets.Allocate(c.Request.Context(), id, req.UserID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, alloc)
}

func (h *handlers) returnAsset(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	alloc, err := h.assets.Return(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, alloc)
}

func (h *handlers) updateAssetStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req struct {
		Status service.AssetStatus `json:"status"`
	}
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.assets.UpdateStatus(c.Request.Context(), id, req.Status)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// bindJSON decodes a size-limited JSON body that may not contain unknown
// fields. On failure it writes a 400 and returns false.
func bindJSON(c *gin.Context, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		abortWithError(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON: "+err.Error())
		return false
	}
	return true
}

func pathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		abortWithError(c, http.StatusBadRequest, "invalid_input", "id must be a positive integer")
		return 0, false
	}
	return id, true
}

// queryInt parses an optional integer query parameter; absent means 0.
func queryInt(c *gin.Context, name string) (int, bool) {
	v := c.Query(name)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		abortWithError(c, http.StatusBadRequest, "invalid_input", name+" must be an integer")
		return 0, false
	}
	return n, true
}
