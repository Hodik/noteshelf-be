#!/bin/sh
set -e

# Wait for Postgres to be ready
echo "Waiting for PostgreSQL to be ready..."
for i in $(seq 1 30); do
  pg_isready -h postgres -p 5432 -U postgres && break
  echo "Waiting for PostgreSQL to be ready... $i/30"
  sleep 1
done

# Run migrations
echo "Running database migrations..."
migrate -path ./migrations -database "${POSTGRESQL_URL}?sslmode=disable" up

# Start the application
echo "Starting application..."
exec ./tmp/app "$@"
