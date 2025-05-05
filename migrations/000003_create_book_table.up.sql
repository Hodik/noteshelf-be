CREATE TABLE IF NOT EXISTS books(
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title VARCHAR(255) NOT NULL,
  author VARCHAR(255),
  owner_id VARCHAR(50) NOT NULL,
  s3_key VARCHAR(255) NOT NULL,

  total_pages INTEGER NOT NULL,
  FOREIGN KEY (owner_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS reading_progress(
  user_id VARCHAR(50),
  book_id UUID,
  current_page INTEGER NOT NULL DEFAULT 1,
  percentage_complete DECIMAL(5, 2) NOT NULL DEFAULT 0,
  last_read_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(user_id, book_id),
  FOREIGN KEY (user_id) REFERENCES users(id),
  FOREIGN KEY (book_id) REFERENCES books(id)
);
