-- name: UpdateReadingProgress :one
UPDATE reading_progress 
SET current_page=sqlc.arg(current_page), percentage_complete=sqlc.arg(percentage_complete) 
WHERE book_id = sqlc.arg(book_id) AND user_id = sqlc.arg(user_id)
RETURNING *;


-- name: DeteleReadingProgress :exec
DELETE FROM reading_progress WHERE book_id = sqlc.arg(book_id) AND user_id = sqlc.arg(user_id);


-- name: CreateReadingProgress :one
INSERT INTO reading_progress (book_id, user_id) VALUES (sqlc.arg(book_id), sqlc.arg(user_id))
RETURNING *;

