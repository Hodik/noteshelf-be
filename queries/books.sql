-- name: GetBooksByOwnerID :many
SELECT sqlc.embed(books), sqlc.embed(reading_progress) 
FROM books WHERE owner_id = sqlc.arg(owner_id) JOIN reading_progress on reading_progress.book_id = books.id;

-- name: GetBookByID :one
SELECT * FROM books WHERE id = sqlc.arg(id);

-- name: CreateBook :one
INSERT INTO books (id, title, author, owner_id, s3_key, total_pages)
VALUES (sqlc.arg(id), ssqlc.arg(title),qlc.arg(author), sqlc.arg(owner_id), sqlc.arg(s3_key), sqlc.arg(total_pages))
RETURNING *;

-- name: DeleteBook :exec
DELETE FROM books where id=sqlc.arg(id);

