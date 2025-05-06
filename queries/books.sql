-- name: GetBooksByOwnerID :many
SELECT sqlc.embed(books), reading_progress.current_page, reading_progress.percentage_complete 
FROM books 
LEFT JOIN reading_progress on reading_progress.book_id = books.id
WHERE owner_id = sqlc.arg(owner_id);

-- name: GetBookByID :one
SELECT * FROM books WHERE id = sqlc.arg(id);

-- name: CreateBook :one
INSERT INTO books (id, title, author, owner_id, s3_key, total_pages)
VALUES (sqlc.arg(id), sqlc.arg(title), sqlc.arg(author), sqlc.arg(owner_id), sqlc.arg(s3_key), sqlc.arg(total_pages))
RETURNING *;

-- name: DeleteBook :exec
DELETE FROM books where id=sqlc.arg(id);

