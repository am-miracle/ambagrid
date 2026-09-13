FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ingestion-go .

FROM alpine:3.22

WORKDIR /app
COPY --from=build /out/ingestion-go /app/ingestion-go

ENTRYPOINT ["/app/ingestion-go"]
