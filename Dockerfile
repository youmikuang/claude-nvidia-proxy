# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download        # 缓存依赖层

COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o claude-nvidia-proxy ./cmd/proxy

# Runtime stage
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/claude-nvidia-proxy /claude-nvidia-proxy
COPY config.json /app/config.json
ENV CONFIG_PATH=/app/config.json
EXPOSE 3002
ENTRYPOINT ["/claude-nvidia-proxy"]