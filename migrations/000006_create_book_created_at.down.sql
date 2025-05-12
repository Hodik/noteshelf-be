DROP TRIGGER IF EXISTS trigger_set_updated_at ON users;
DROP TRIGGER IF EXISTS trigger_set_updated_at ON books;

DROP FUNCTION IF EXISTS set_updated_at;

ALTER TABLE books
DROP COLUMN IF EXISTS added_at,
DROP COLUMN IF EXISTS updated_at;

