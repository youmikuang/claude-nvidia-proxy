# claude-nvidia-proxy (Go)

A proxy server that converts Anthropic/Claude-style API requests to OpenAI Chat Completions format and forwards them to NVIDIA's hidden models.

## Models Available

NVIDIA provides two hidden models accessible via this proxy:
- `z-ai/glm4.7` - Powerful general-purpose model
- `minimaxai/minimax-m2.1` - Fast inference model

## Quick Start

### Prerequisites

1. Visit https://build.nvidia.com/explore/discover
2. Register an account and generate an API key
3. Copy `config.json.example` to `config.json` and add your API key

### Run with Docker (Recommended)

```bash
# Build and run
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

### Run with Go

```bash
go run cmd/proxy/main.go
```

The server will start on port 3001.

## Configuration

### Config File (`config.json`)

```json
{
  "nvidia_url": "https://integrate.api.nvidia.com/v1/chat/completions",
  "nvidia_key": "your-nvidia-api-key-here"
}
```

**Note:** Do not commit your real `nvidia_key` to version control.

### Environment Variables (Optional Overrides)

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `config.json` | Path to config file |
| `PROVIDER_API_KEY` | - | Overrides `nvidia_key` from config |
| `UPSTREAM_URL` | NVIDIA API URL | Overrides `nvidia_url` from config |
| `SERVER_API_KEY` | - | Enable inbound auth |
| `ADDR` | `:3001` | Server listen address |
| `UPSTREAM_TIMEOUT_SECONDS` | `300` | Request timeout |
| `LOG_BODY_MAX_CHARS` | `4096` | Max body chars in logs (0 to disable) |
| `LOG_STREAM_TEXT_PREVIEW_CHARS` | `256` | Stream preview length (0 to disable) |

## Docker Deployment

### Basic

```bash
docker compose up -d
```

### With Environment Variables

```yaml
services:
  claude-nvidia-proxy:
    image: claude-nvidia-proxy
    container_name: claude-nvidia-proxy
    ports:
      - "3001:3001"
    environment:
      - PROVIDER_API_KEY=${NVIDIA_API_KEY}
      - SERVER_API_KEY=your-server-api-key
      - TZ=Asia/Shanghai
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:3001/"]
      interval: 30s
      timeout: 10s
      retries: 3
```

### Build Image Manually

```bash
docker build -t claude-nvidia-proxy .
```

## Usage with Claude Code

### Using z-ai/glm4.7

```bash
export ANTHROPIC_BASE_URL=http://localhost:3001
export ANTHROPIC_AUTH_TOKEN=your-nvidia-api-key
export ANTHROPIC_DEFAULT_HAIKU_MODEL=z-ai/glm4.7
export ANTHROPIC_DEFAULT_SONNET_MODEL=z-ai/glm4.7
export ANTHROPIC_DEFAULT_OPUS_MODEL=z-ai/glm4.7

claude
```

### Using minimaxai/minimax-m2.1

```bash
export ANTHROPIC_BASE_URL=http://localhost:3001
export ANTHROPIC_AUTH_TOKEN=your-nvidia-api-key
export ANTHROPIC_DEFAULT_HAIKU_MODEL=minimaxai/minimax-m2.1
export ANTHROPIC_DEFAULT_SONNET_MODEL=minimaxai/minimax-m2.1
export ANTHROPIC_DEFAULT_OPUS_MODEL=minimaxai/minimax-m2.1

claude
```

## API Reference

### POST /v1/messages

Accepts Anthropic/Claude style message format, converts to OpenAI format, and proxies to NVIDIA.

**Authentication:**
- Inbound: `Authorization: Bearer <SERVER_API_KEY>` or `x-api-key: <SERVER_API_KEY>` (if `SERVER_API_KEY` is set)
- Outbound: Always sends `Authorization: Bearer <nvidia_key>` to NVIDIA

**Request (non-streaming):**

```bash
curl -sS http://127.0.0.1:3001/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"z-ai/glm4.7",
    "max_tokens":256,
    "messages":[{"role":"user","content":"hello"}]
  }'
```

**Request (streaming):**

```bash
curl -N http://127.0.0.1:3001/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"z-ai/glm4.7",
    "max_tokens":256,
    "stream":true,
    "messages":[{"role":"user","content":"hello"}]
  }'
```

## Build from Source

### Linux (amd64)

```bash
mkdir -p dist
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/claude-nvidia-proxy_linux_amd64 .
```

### Windows (amd64)

```bash
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/claude-nvidia-proxy_windows_amd64.exe .
```

### macOS (arm64)

```bash
mkdir -p dist
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o dist/claude-nvidia-proxy_darwin_arm64 .
```

## Notes & Limitations

- Streaming conversion supports `delta.content` text and `delta.tool_calls` tool-use blocks
- Other Anthropic blocks are not fully implemented
- Logs show forwarded request bodies; keep `LOG_BODY_MAX_CHARS` small and avoid secrets in prompts

## Project Structure

```
.
├── cmd/
│   └── proxy/
│       └── main.go          # Application entrypoint
├── internal/
│   ├── config/              # Configuration loading
│   ├── converter/           # API format conversion
│   ├── logging/             # Logging utilities
│   ├── server/              # HTTP server handlers
│   └── types/               # Type definitions
├── Dockerfile
├── docker-compose.yml
├── config.json
├── go.mod
└── README.md
```
