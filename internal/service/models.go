package service

import (
	"encoding/json"
	"time"

	"github.com/jenish0908/assetflow-be/internal/auth"
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

// AssetFilter narrows an asset listing. Zero values mean "no filter".
type AssetFilter struct {
	Status AssetStatus
	Query  string // matched against tag and name, case-insensitive
}

type UserPage struct {
	Items  []User `json:"items"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Total  int64  `json:"total"`
}

// Stats is the overview screen's summary of the whole system.
type Stats struct {
	Assets            int64                     `json:"assets"`
	AssetsByStatus    map[AssetStatus]int64     `json:"assets_by_status"`
	ActiveAllocations int64                     `json:"active_allocations"`
	Users             int64                     `json:"users"`
	JobsByStatus      map[string]int64          `json:"jobs_by_status"`
	PaymentsByStatus  map[string]PaymentSummary `json:"payments_by_status"`
}

type PaymentSummary struct {
	Count       int64 `json:"count"`
	AmountCents int64 `json:"amount_cents"`
}

// AllocationEvent is an allocation with the asset and person named, for
// activity lists.
type AllocationEvent struct {
	ID          int64      `json:"id"`
	AssetID     int64      `json:"asset_id"`
	AssetTag    string     `json:"asset_tag"`
	AssetName   string     `json:"asset_name"`
	UserID      int64      `json:"user_id"`
	UserName    string     `json:"user_name"`
	AllocatedAt time.Time  `json:"allocated_at"`
	ReturnedAt  *time.Time `json:"returned_at,omitempty"`
}

// Notification is a notification job as shown in the feed.
type Notification struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	RunAt       time.Time       `json:"run_at"`
	LastError   *string         `json:"last_error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
