# logstack

Centralized logging service for the inber/si ecosystem.

## Features

- **Unified log format**: Standard JSON structure for all services
- **File-based storage**: JSONL files organized by date and source
- **REST API**: Ingest, query, group, and analyze logs
- **Multiple output formats**: JSON, JSONL, text, table, logfmt

## API Endpoints

### Ingest Logs

```bash
# Single log entry
POST /api/v1/logs
{
  "source": "si",
  "agent": "task-manager",
  "level": "info",
  "type": "message",
  "content": "User said hello",
  "model": "opus46",
  "tokens_in": 150,
  "tokens_out": 50,
  "latency_ms": 1200
}

# Batch ingestion
POST /api/v1/logs/batch
[
  { "source": "inber", ... },
  { "source": "si", ... }
]
```

### Query Logs

```bash
# Search with filters
GET /api/v1/logs?source=si&level=error&limit=50

# Get specific log
GET /api/v1/logs/{id}
```

### Aggregation

```bash
# Group by field
GET /api/v1/logs/group/model
GET /api/v1/logs/group/source
GET /api/v1/logs/group/day

# Get statistics
GET /api/v1/stats?source=inber&from=2026-03-01T00:00:00Z
```

## Log Format

```json
{
  "id": "uuid",
  "timestamp": "2026-03-03T16:00:00Z",
  "source": "si",
  "agent": "task-manager",
  "channel": "discord",
  "session_id": "session-123",
  "model": "opus46",
  "level": "info",
  "type": "message",
  "content": "...",
  "tokens_in": 150,
  "tokens_out": 50,
  "latency_ms": 1200,
  "error": "",
  "metadata": {}
}
```

## Storage Structure

```
logs/
├── 2026-03-01/
│   ├── inber.jsonl
│   ├── si.jsonl
│   └── claxon-android.jsonl
├── 2026-03-02/
│   └── ...
└── 2026-03-03/
    └── ...
```

## Running

```bash
# Build
go build -o ~/bin/logstack ./cmd/logstack

# Run
LOGSTACK_PORT=8081 LOGSTACK_DATA_DIR=~/logs logstack

# Or with defaults
~/bin/logstack
```

## Integration

### From si (Go)

```go
import "github.com/kayushkin/logstack/internal/client"

client := client.New("http://localhost:8081")
client.Log(models.LogEntry{
    Source:    "si",
    Level:     models.LevelInfo,
    Type:      models.TypeMessage,
    Content:   "Message routed to inber",
    Model:     "opus46",
})
```

### From inber (Go)

```go
// inber writes to logstack via HTTP after each turn
client.Log(models.LogEntry{
    Source:    "inber",
    Agent:     "task-manager",
    Type:      models.TypeMetrics,
    TokensIn:  result.InputTokens,
    TokensOut: result.OutputTokens,
    LatencyMs: elapsed.Milliseconds(),
})
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LOGSTACK_PORT` | 8081 | Server port |
| `LOGSTACK_DATA_DIR` | ./logs | Log storage directory |
| `GIN_MODE` | release | Gin framework mode |
