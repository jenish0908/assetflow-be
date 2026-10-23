-- name: CreateAllocation :one
INSERT INTO allocations (asset_id, user_id)
VALUES ($1, $2)
RETURNING *;

-- name: CloseActiveAllocation :one
UPDATE allocations SET returned_at = now()
WHERE asset_id = $1 AND returned_at IS NULL
RETURNING *;

-- name: ListAllocationsByAsset :many
SELECT * FROM allocations
WHERE asset_id = $1
ORDER BY allocated_at DESC, id DESC;

-- ListAllocationsByAssetIDs loads allocations for a whole page of assets in
-- one round trip. The IDs travel as a single array parameter, so the SQL text
-- (and its prepared statement) is the same for any number of IDs.
-- name: ListAllocationsByAssetIDs :many
SELECT * FROM allocations
WHERE asset_id = ANY(sqlc.arg(asset_ids)::bigint[])
ORDER BY allocated_at DESC, id DESC;
