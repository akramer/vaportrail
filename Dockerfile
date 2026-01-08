FROM rust:1.75-slim as builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y \
    libssl-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

# Copy source
COPY . .

# Build release binary
RUN cargo build --release

# Runtime image
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    ca-certificates \
    iputils-ping \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/target/release/vaportrail /app/vaportrail

# Create data directory
RUN mkdir -p /data

ENV VAPORTRAIL_DB_PATH=/data/vaportrail.db
ENV VAPORTRAIL_HTTP_PORT=8080

EXPOSE 8080

CMD ["/app/vaportrail"]
