CREATE TABLE IF NOT EXISTS pdf_references(
  id UUID PRIMARY KEY,

  page_number SMALLINT NOT NULL,
  x_start REAL NOT NULL,
  x_end REAL,

  y_start REAL NOT NULL,
  y_end REAL
);


CREATE TABLE IF NOT EXISTS notes(
  id UUID PRIMARY KEY,
  book_id UUID NOT NULL,
  user_id VARCHAR(50) NOT NULL,
  content TEXT,
  color VARCHAR(7) DEFAULT '#FFE066',
  added_at TIMESTAMP NOT NULL DEFAULT NOW() ,
  updated_at TIMESTAMP NOT NULL DEFAULT NOW(),

  reference_type VARCHAR(20),
  reference_data_pdf UUID,


  FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (reference_data_pdf) REFERENCES pdf_references(id) ON DELETE SET NULL
);

CREATE INDEX idx_notes_book_user_added_at ON notes(book_id, user_id, added_at DESC);

CREATE TRIGGER trigger_set_updated_at
BEFORE UPDATE ON notes
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
