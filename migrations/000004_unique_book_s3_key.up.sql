ALTER TABLE books
ADD CONSTRAINT unique_s3_key UNIQUE (s3_key);
