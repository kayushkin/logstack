# Logstack Agent

Centralized logging service for the inber/si ecosystem.

## Purpose

Maintain and extend the logstack service — unified logging, REST API, storage, formatting.

## Agent Config

- **Name:** logstack
- **Model:** sonnet-4-5 (claude-sonnet-4-5)
- **Role:** Backend service development, API design, Go code

## Responsibilities

- Add new API endpoints
- Extend log format with new fields
- Optimize storage and querying
- Add new output formats
- Integrate with inber/si for log ingestion
- Performance improvements
- Testing

## Always Deploy After Push

Any push to main should trigger a rebuild of `~/bin/logstack`.

## Commands

```bash
# Build
go build -o ~/bin/logstack ./cmd/logstack

# Run
~/bin/logstack

# Test
go test ./...
```

## Integration Points

- **inber**: Should send logs after each turn
- **si**: Should send routing/error logs
- **claxon-android**: Could send device logs via HTTP

## API Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/logs` | POST | Ingest single log |
| `/api/v1/logs/batch` | POST | Batch ingestion |
| `/api/v1/logs` | GET | Query with filters |
| `/api/v1/logs/:id` | GET | Get specific log |
| `/api/v1/logs/group/:field` | GET | Group by field |
| `/api/v1/stats` | GET | Aggregate statistics |

## File Structure

```
logstack/
├── cmd/logstack/main.go       # Entry point
├── internal/
│   ├── api/handler.go         # HTTP handlers
│   ├── store/store.go         # File-based storage
│   ├── format/format.go       # Output formatters
│   ├── models/log.go          # Log entry model
│   └── client/client.go       # Go client for integration
├── go.mod
└── README.md
```
