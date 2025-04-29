-- name: GetAllUsers :many
SELECT * FROM users;


-- name: GetUserById :one
SELECT * FROM users
WHERE id = sqlc.arg(id);

-- name: CreateUser :one
INSERT INTO users (id, username, first_name, last_name, added_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(username), sqlc.arg(first_name), sqlc.arg(last_name), sqlc.arg(added_at), sqlc.arg(updated_at))
RETURNING *;


