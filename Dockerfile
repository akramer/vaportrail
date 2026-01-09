FROM debian:bookworm-slim as builder

WORKDIR /app

# Install build dependencies and Rust
RUN apt-get update && apt-get install -y \
    curl \
    build-essential \
    libssl-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/* \
    && curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain 1.83.0

ENV PATH="/root/.cargo/bin:${PATH}"

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
