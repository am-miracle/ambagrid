# Staging-only: runs simulator, ingestion, engine and outbox publisher in one
# container so staging costs a single Render worker. Production keeps them separate.
# Build context must be the repo root.
FROM rust:1.98-slim-bookworm AS engine

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    pkg-config \
    libssl-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY proto ./proto
COPY services/engine-rust ./services/engine-rust

WORKDIR /src/services/engine-rust
RUN cargo build --release

FROM golang:1.27-bookworm AS go

WORKDIR /src/services/ingestion-go
COPY services/ingestion-go/go.mod services/ingestion-go/go.sum ./
RUN go mod download
COPY services/ingestion-go ./
RUN CGO_ENABLED=0 go build -o /out/ingestion-go .

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY scripts ./scripts
RUN CGO_ENABLED=0 go build -o /out/virtual-meter ./scripts

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libssl3 \
    postgresql-client \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home ambagrid

WORKDIR /app
COPY --from=engine /src/services/engine-rust/target/release/engine-rust /app/engine-rust
COPY --from=go /out/ingestion-go /app/ingestion-go
COPY --from=go /out/virtual-meter /app/virtual-meter
COPY deploy/staging-worker/run.sh deploy/staging-worker/seed.sql /app/

USER ambagrid

ENTRYPOINT ["/app/run.sh"]
