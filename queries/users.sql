-- name: GetAllUsers :many
SELECT * FROM users;


-- name: GetUserById :one
SELECT * FROM users
WHERE id = sqlc.arg(id);

-- name: CreateUser :one
INSERT INTO users (id, username, first_name, last_name, email, phone)
VALUES (sqlc.arg(id), sqlc.arg(username), sqlc.arg(first_name), sqlc.arg(last_name), sqlc.arg(email), sqlc.arg(phone))
RETURNING *;


