-- name: RegisterWaitListEmail :one
INSERT INTO wait_list_emails (email)
VALUES (sqlc.arg(email))
RETURNING *;
