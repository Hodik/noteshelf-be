-- name: GetNotesForBookUser :many
SELECT sqlc.embed(notes), pdf_references.id, pdf_references.page_number, pdf_references.x_start, pdf_references.x_end, pdf_references.y_start, pdf_references.y_end
FROM notes 
LEFT JOIN pdf_references on notes.reference_data_pdf = pdf_references.id
WHERE book_id = sqlc.arg(book_id) and user_id = sqlc.arg(user_id)
ORDER BY added_at DESC;

-- name: GetNoteByID :one
SELECT sqlc.embed(notes), pdf_references.id, pdf_references.page_number, pdf_references.x_start, pdf_references.x_end, pdf_references.y_start, pdf_references.y_end
FROM notes 
LEFT JOIN pdf_references on notes.reference_data_pdf = pdf_references.id
WHERE notes.id = sqlc.arg(id)
LIMIT 1;

-- name: CreatePDFReference :one
INSERT INTO pdf_references (id, page_number, x_start, x_end, y_start, y_end)
VALUES (sqlc.arg(id), sqlc.arg(page_number), sqlc.arg(x_start), sqlc.narg(x_end), sqlc.arg(y_start), sqlc.narg(y_end))
RETURNING *;

-- name: CreateNote :one
INSERT INTO notes (id, book_id, user_id, content, color, reference_type, reference_data_pdf)
VALUES (sqlc.arg(id), sqlc.arg(book_id), sqlc.arg(user_id), sqlc.arg(content), sqlc.arg(color), sqlc.narg(reference_type), sqlc.narg(reference_data_pdf_id))
RETURNING *;

-- name: UpdateNote :one
UPDATE notes SET content=sqlc.arg(content), color=sqlc.arg(color)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteNote :exec
DELETE FROM notes WHERE id=sqlc.arg(id);
