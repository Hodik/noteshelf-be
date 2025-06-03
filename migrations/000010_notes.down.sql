DROP TRIGGER IF EXISTS trigger_set_updated_at ON notes;

DROP INDEX IF EXISTS idx_notes_reference_data_gin;
DROP INDEX IF EXISTS idx_notes_added_at; 
DROP INDEX IF EXISTS idx_notes_reference_type;
DROP INDEX IF EXISTS idx_notes_book_user;

DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS pdf_references;
