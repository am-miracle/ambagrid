# Build context must be the repo root: build.rs reads
# ../../proto/telemetry.proto relative to this crate.
FROM rust:1.98-slim-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    pkg-config \
    librdkafka-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY proto ./proto
COPY services/engine-rust ./services/engine-rust

WORKDIR /src/services/engine-rust
RUN cargo build --release

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    librdkafka1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /src/services/engine-rust/target/release/engine-rust /app/engine-rust

ENTRYPOINT ["/app/engine-rust"]
