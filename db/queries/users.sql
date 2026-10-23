-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users
WHERE id = $1;

-- FindUsers matches an email or an exact display name, case-insensitively.
-- name: FindUsers :many
SELECT * FROM users
WHERE lower(email) = lower(sqlc.arg(query)) OR lower(name) = lower(sqlc.arg(query))
ORDER BY id
LIMIT 5;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1;

-- ListUsers is the people directory; query is an escaped ILIKE pattern or NULL.
-- name: ListUsers :many
SELECT * FROM users
WHERE sqlc.narg(query)::text IS NULL OR email ILIKE sqlc.narg(query) OR name ILIKE sqlc.narg(query)
ORDER BY lower(name), id
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountUsers :one
SELECT count(*) FROM users
WHERE sqlc.narg(query)::text IS NULL OR email ILIKE sqlc.narg(query) OR name ILIKE sqlc.narg(query);

-- ListUsersByIDs resolves the people on a page of assets in one query
-- (the same batching idea as ListAllocationsByAssetIDs).
-- name: ListUsersByIDs :many
SELECT * FROM users
WHERE id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id;
