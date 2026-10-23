package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/service"
)

// AssetService is what the tools need from the asset service.
type AssetService interface {
	Create(ctx context.Context, in service.NewAsset) (service.Asset, error)
	GetByTag(ctx context.Context, tag string) (service.AssetWithAllocations, error)
	Update(ctx context.Context, id int64, p service.AssetPatch) (service.Asset, error)
	Allocate(ctx context.Context, assetID, userID int64) (service.Allocation, error)
	UpdateStatus(ctx context.Context, id int64, status service.AssetStatus) (service.Asset, error)
}

// UserService is what the tools need from the user service.
type UserService interface {
	Create(ctx context.Context, in service.CreateUserInput) (service.User, error)
	Find(ctx context.Context, query string) ([]service.User, error)
	Get(ctx context.Context, id int64) (service.User, error)
}

// tool is one operation the model may request. minRole mirrors the HTTP
// route's RBAC rule, so the agent can never do more than the caller could
// do through the API directly.
type tool struct {
	def      ToolDef
	minRole  auth.Role
	readOnly bool
	run      func(ctx context.Context, args string) (any, error)
}

// typed wraps a handler taking a typed argument struct. The model's JSON is
// decoded strictly: malformed JSON, wrong types and unknown fields are
// rejected before any service is called.
func typed[A any](handle func(ctx context.Context, args A) (any, error)) func(context.Context, string) (any, error) {
	return func(ctx context.Context, raw string) (any, error) {
		var args A
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&args); err != nil {
			return nil, service.NewError(service.ErrInvalid, "invalid arguments: %v", err)
		}
		return handle(ctx, args)
	}
}

func required(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return service.NewError(service.ErrInvalid, "invalid arguments: %s is required", field)
	}
	return nil
}

func newTools(assets AssetService, users UserService) []tool {
	// resolveAsset and resolveUser turn the names people use (tags, emails,
	// display names) into IDs, with errors the model can act on.
	resolveAsset := func(ctx context.Context, tag string) (service.AssetWithAllocations, error) {
		if err := required("asset_tag", tag); err != nil {
			return service.AssetWithAllocations{}, err
		}
		return assets.GetByTag(ctx, tag)
	}
	resolveUser := func(ctx context.Context, who string) (service.User, error) {
		if err := required("user", who); err != nil {
			return service.User{}, err
		}
		found, err := users.Find(ctx, who)
		if err != nil {
			return service.User{}, err
		}
		switch len(found) {
		case 0:
			return service.User{}, service.NewError(service.ErrNotFound, "no user matches %q; ask for their email", who)
		case 1:
			return found[0], nil
		}
		emails := make([]string, len(found))
		for i, u := range found {
			emails[i] = u.Email
		}
		return service.User{}, service.NewError(service.ErrInvalid,
			"%q matches %d users (%s); ask which one", who, len(found), strings.Join(emails, ", "))
	}

	return []tool{
		{
			def: ToolDef{
				Name:        "getAssetInfo",
				Description: "Look up an asset by tag: name, category, status, who has it now, and allocation history.",
				Parameters:  schema(`{"asset_tag": {"type": "string", "description": "Asset tag, e.g. L-102"}}`, "asset_tag"),
			},
			minRole:  auth.RoleViewer,
			readOnly: true,
			run: typed(func(ctx context.Context, a struct {
				AssetTag string `json:"asset_tag"`
			}) (any, error) {
				asset, err := resolveAsset(ctx, a.AssetTag)
				if err != nil {
					return nil, err
				}
				// Name the current holder, so the answer can say "Priya Shah"
				// rather than "user #3".
				result := struct {
					service.AssetWithAllocations
					Holder *holder `json:"holder,omitempty"`
				}{AssetWithAllocations: asset}
				for _, al := range asset.Allocations {
					if al.ReturnedAt == nil {
						if u, err := users.Get(ctx, al.UserID); err == nil {
							result.Holder = &holder{ID: u.ID, Name: u.Name, Email: u.Email}
						}
						break
					}
				}
				return result, nil
			}),
		},
		{
			def: ToolDef{
				Name:        "createAsset",
				Description: "Create a new asset.",
				Parameters: schema(`{
					"tag": {"type": "string", "description": "Unique tag, e.g. L-200"},
					"name": {"type": "string"},
					"category": {"type": "string", "description": "e.g. laptop, monitor, phone"}}`,
					"tag", "name", "category"),
			},
			minRole: auth.RoleManager,
			run: typed(func(ctx context.Context, a service.NewAsset) (any, error) {
				return assets.Create(ctx, a)
			}),
		},
		{
			def: ToolDef{
				Name:        "updateAsset",
				Description: "Rename an asset or change its category.",
				Parameters: schema(`{
					"asset_tag": {"type": "string"},
					"name": {"type": "string"},
					"category": {"type": "string"}}`,
					"asset_tag"),
			},
			minRole: auth.RoleManager,
			run: typed(func(ctx context.Context, a struct {
				AssetTag string  `json:"asset_tag"`
				Name     *string `json:"name"`
				Category *string `json:"category"`
			}) (any, error) {
				asset, err := resolveAsset(ctx, a.AssetTag)
				if err != nil {
					return nil, err
				}
				return assets.Update(ctx, asset.ID, service.AssetPatch{Name: a.Name, Category: a.Category})
			}),
		},
		{
			def: ToolDef{
				Name:        "allocateAsset",
				Description: "Allocate an available asset to a user.",
				Parameters: schema(`{
					"asset_tag": {"type": "string"},
					"user": {"type": "string", "description": "The user's email, or their exact full name"}}`,
					"asset_tag", "user"),
			},
			minRole: auth.RoleManager,
			run: typed(func(ctx context.Context, a struct {
				AssetTag string `json:"asset_tag"`
				User     string `json:"user"`
			}) (any, error) {
				asset, err := resolveAsset(ctx, a.AssetTag)
				if err != nil {
					return nil, err
				}
				user, err := resolveUser(ctx, a.User)
				if err != nil {
					return nil, err
				}
				alloc, err := assets.Allocate(ctx, asset.ID, user.ID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"allocation": alloc, "asset_tag": asset.Tag, "user": user.Name, "email": user.Email}, nil
			}),
		},
		{
			def: ToolDef{
				Name:        "updateAssetStatus",
				Description: "Set an asset's status. Use allocateAsset to allocate; an allocated asset must be returned first.",
				Parameters: schema(`{
					"asset_tag": {"type": "string"},
					"status": {"type": "string", "enum": ["available", "maintenance", "retired"]}}`,
					"asset_tag", "status"),
			},
			minRole: auth.RoleManager,
			run: typed(func(ctx context.Context, a struct {
				AssetTag string              `json:"asset_tag"`
				Status   service.AssetStatus `json:"status"`
			}) (any, error) {
				asset, err := resolveAsset(ctx, a.AssetTag)
				if err != nil {
					return nil, err
				}
				return assets.UpdateStatus(ctx, asset.ID, a.Status)
			}),
		},
		{
			def: ToolDef{
				Name:        "addUser",
				Description: "Create a user account. A random initial password is generated and is never shown in this chat.",
				Parameters: schema(`{
					"email": {"type": "string"},
					"name": {"type": "string"},
					"role": {"type": "string", "enum": ["admin", "manager", "viewer"]}}`,
					"email", "name", "role"),
			},
			minRole: auth.RoleAdmin,
			run: typed(func(ctx context.Context, a struct {
				Email string    `json:"email"`
				Name  string    `json:"name"`
				Role  auth.Role `json:"role"`
			}) (any, error) {
				// The password must not enter the model's context: the
				// conversation is sent to a third party and may be logged.
				return users.Create(ctx, service.CreateUserInput{Email: a.Email, Name: a.Name, Role: a.Role, Password: randomPassword()})
			}),
		},
	}
}

// schema builds a JSON Schema object with the given properties.
func schema(properties string, requiredFields ...string) json.RawMessage {
	req, _ := json.Marshal(requiredFields)
	s := fmt.Sprintf(`{"type": "object", "properties": %s, "required": %s, "additionalProperties": false}`, properties, req)
	if !json.Valid([]byte(s)) {
		panic("agent: invalid tool schema: " + s) // programmer error, caught by tests
	}
	return json.RawMessage(s)
}

func randomPassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// holder is the person who has an asset right now, as shown to the model.
type holder struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
