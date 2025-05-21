CREATE TABLE IF NOT EXISTS book_viewers(
  book_id UUID,
  user_id VARCHAR(50),
  PRIMARY KEY(book_id, user_id)
);
