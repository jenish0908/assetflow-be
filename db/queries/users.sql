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
