FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/edge-agent-go .

FROM alpine:3.22

RUN adduser -D -u 10001 edge
WORKDIR /app
COPY --from=build /out/edge-agent-go /app/edge-agent-go
RUN mkdir -p /var/lib/ambagrid-edge && chown edge:edge /var/lib/ambagrid-edge

USER edge
EXPOSE 2112
ENTRYPOINT ["/app/edge-agent-go"]
