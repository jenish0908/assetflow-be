-- name: CreateAsset :one
INSERT INTO assets (tag, name, category)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAsset :one
SELECT * FROM assets
WHERE id = $1;

-- name: GetAssetByTag :one
SELECT * FROM assets
WHERE tag = $1;

-- GetAssetForUpdate locks the row until the transaction ends, so two
-- concurrent allocations of the same asset are serialised.
-- name: GetAssetForUpdate :one
SELECT * FROM assets
WHERE id = $1
FOR UPDATE;

-- name: ListAssets :many
SELECT * FROM assets
ORDER BY id
LIMIT $1 OFFSET $2;

-- The *Filtered queries serve searches. They are separate statements on
-- purpose: a catch-all "($1 IS NULL OR col = $1)" predicate is evaluated on
-- every row in a generic plan, which made the unfiltered list (the hot path
-- measured in docs/perf.md) measurably slower. NULL means "no filter";
-- query is an ILIKE pattern built and escaped by the service.
-- name: ListAssetsFiltered :many
SELECT * FROM assets
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(query)::text IS NULL OR tag ILIKE sqlc.narg(query) OR name ILIKE sqlc.narg(query))
ORDER BY id
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- ListAssetsWithAllocations is the JOIN alternative to the batched query.
-- The page is cut in a subquery *before* the join: putting LIMIT on the
-- joined rows would count allocations, not assets, and split an asset's
-- allocations across pages. Asset columns repeat once per allocation row.
-- name: ListAssetsWithAllocations :many
SELECT
  a.id, a.tag, a.name, a.category, a.status, a.created_at, a.updated_at,
  al.id           AS allocation_id,
  al.user_id      AS allocation_user_id,
  al.allocated_at AS allocation_allocated_at,
  al.returned_at  AS allocation_returned_at
FROM (
  SELECT * FROM assets
  ORDER BY id
  LIMIT $1 OFFSET $2
) AS a
LEFT JOIN allocations al ON al.asset_id = a.id
ORDER BY a.id, al.allocated_at DESC, al.id DESC;

-- name: ListAssetsWithAllocationsFiltered :many
SELECT
  a.id, a.tag, a.name, a.category, a.status, a.created_at, a.updated_at,
  al.id           AS allocation_id,
  al.user_id      AS allocation_user_id,
  al.allocated_at AS allocation_allocated_at,
  al.returned_at  AS allocation_returned_at
FROM (
  SELECT * FROM assets
  WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
    AND (sqlc.narg(query)::text IS NULL OR tag ILIKE sqlc.narg(query) OR name ILIKE sqlc.narg(query))
  ORDER BY id
  LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off)
) AS a
LEFT JOIN allocations al ON al.asset_id = a.id
ORDER BY a.id, al.allocated_at DESC, al.id DESC;

-- name: CountAssets :one
SELECT count(*) FROM assets;

-- name: CountAssetsFiltered :one
SELECT count(*) FROM assets
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(query)::text IS NULL OR tag ILIKE sqlc.narg(query) OR name ILIKE sqlc.narg(query));

-- name: UpdateAsset :one
UPDATE assets SET
  name       = COALESCE(sqlc.narg(name), name),
  category   = COALESCE(sqlc.narg(category), category),
  updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetAssetStatus :one
UPDATE assets SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteAsset :execrows
DELETE FROM assets
WHERE id = $1;
