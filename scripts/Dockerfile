FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY scripts ./scripts
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/virtual-meter ./scripts

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 simulator

WORKDIR /app
COPY --from=build /out/virtual-meter /app/virtual-meter

USER simulator

ENTRYPOINT ["/app/virtual-meter"]
