FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api-go .

FROM alpine:3.22

# ca-certificates so a DATABASE_URL with sslmode=verify-full can validate the
# server; the base image ships no trust store.
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 api

WORKDIR /app
COPY --from=build /out/api-go /app/api-go

USER api

EXPOSE 8081

ENTRYPOINT ["/app/api-go"]
