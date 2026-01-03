# Build stage
FROM golang:1.24-bookworm AS builder

# Set working directory
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
# CGO_ENABLED=1 is required for go-sqlite3
# -ldflags="-w -s" reduces binary size
RUN CGO_ENABLED=1 go build -ldflags="-w -s" -o vaportrail ./cmd/vaportrail

# Run stage
FROM debian:bookworm-slim

# Install ca-certificates for HTTPS probes and sqlite3 library dependencies
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/vaportrail .

# Expose the default port
EXPOSE 8080

# Environment variables
ENV VAPORTRAIL_HTTP_PORT=8080
ENV VAPORTRAIL_DB_PATH=/app/data/vaportrail.db

# Create a volume for persistent data
VOLUME ["/app/data"]

# Run the binary
CMD ["./vaportrail"]
