package service

import (
	"time"

	"github.com/jenish0908/assetflow/internal/auth"
)

type AssetStatus string

const (
	StatusAvailable   AssetStatus = "available"
	StatusAllocated   AssetStatus = "allocated"
	StatusMaintenance AssetStatus = "maintenance"
	StatusRetired     AssetStatus = "retired"
)

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      auth.Role `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// UserCredentials is a user together with its password hash. It is only used
// for login and is never serialised.
type UserCredentials struct {
	User
	PasswordHash string `json:"-"`
}

type Asset struct {
	ID        int64       `json:"id"`
	Tag       string      `json:"tag"`
	Name      string      `json:"name"`
	Category  string      `json:"category"`
	Status    AssetStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type Allocation struct {
	ID          int64      `json:"id"`
	AssetID     int64      `json:"asset_id"`
	UserID      int64      `json:"user_id"`
	AllocatedAt time.Time  `json:"allocated_at"`
	ReturnedAt  *time.Time `json:"returned_at,omitempty"`
}

type AssetWithAllocations struct {
	Asset
	Allocations []Allocation `json:"allocations"`
}

type AssetPage struct {
	Items  []AssetWithAllocations `json:"items"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
	Total  int64                  `json:"total"`
}

// NewUser is what the store needs to insert a user.
type NewUser struct {
	Email        string
	Name         string
	PasswordHash string
	Role         auth.Role
}

// NewAsset is the input for creating an asset.
type NewAsset struct {
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// AssetPatch is a partial update; nil fields are left unchanged.
type AssetPatch struct {
	Name     *string `json:"name"`
	Category *string `json:"category"`
}
