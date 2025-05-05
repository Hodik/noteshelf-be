FROM golang:1.23 AS builder

WORKDIR /app

# Copy go module files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o app .

FROM alpine:latest

WORKDIR /app

# Install necessary packages
RUN apk --no-cache add ca-certificates curl postgresql-client bash

# Install migrate tool directly from GitHub releases
RUN curl -L https://github.com/golang-migrate/migrate/releases/download/v4.16.2/migrate.linux-amd64.tar.gz | tar xvz && \
    mv migrate /usr/local/bin/migrate && \
    chmod +x /usr/local/bin/migrate

# Copy migrations
COPY --from=builder /app/migrations ./migrations

# Copy the built binary
COPY --from=builder /app/app .

# Copy the startup script
COPY docker-entrypoint.sh .
RUN chmod +x docker-entrypoint.sh

EXPOSE 8080

# Set entrypoint to run migrations before starting the app
ENTRYPOINT ["./docker-entrypoint.sh"]
