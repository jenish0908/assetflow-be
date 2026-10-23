// Package auth handles roles, password hashing and JWT access tokens.
package auth

// Role is a user's permission level. Roles are ordered: each one includes
// every permission of the roles below it.
type Role string

const (
	RoleViewer  Role = "viewer"
	RoleManager Role = "manager"
	RoleAdmin   Role = "admin"
)

var roleRank = map[Role]int{
	RoleViewer:  1,
	RoleManager: 2,
	RoleAdmin:   3,
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	_, ok := roleRank[r]
	return ok
}

// AtLeast reports whether r has at least the permissions of required.
func (r Role) AtLeast(required Role) bool {
	return r.Valid() && roleRank[r] >= roleRank[required]
}
