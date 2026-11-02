# About logstack

## What it is

A small central log service: other services post log entries in one JSON shape, and logstack keeps them as JSONL files by date and source and answers queries over them. si sends its routing and error logs here (`si/logstack.go`), and inber points at it too. `README.md` shows the entry shape and examples.

## Where this prompt lives

These sections are stored in agent-store as a project prompt collection and rendered, with identical text, to `AGENTS.md` and `CLAUDE.md` at the root of this repo, so that whichever file a harness reads it gets the same thing. Edit them on dash `/files`, or edit either rendered file: the 15-minute scan carries the edit back into the sections and out to the other file. The host prompt keeps one row for this repo with only what an agent elsewhere needs.

# How it works

## Routes and code

Every route is under `/api/v1` (`internal/api/handler.go`, gin): `POST /logs` and `POST /logs/batch` take entries; `GET /logs` queries with filters, `GET /logs/{id}` reads one, `GET /logs/group/{field}` groups them; `GET /stats`, `GET /usage`, `GET /max-usage` and `GET /rate-limits` summarise; `GET /health`. `internal/store` keeps the files, `internal/format` writes JSON, JSONL, text, table and logfmt, `models/log.go` is the entry, and `client/` is the Go client other services import. Settings are declared with llm-bridge `servicesettings` in `internal/config/settings.go` (`LOGSTACK_LISTEN_HOST`, `LOGSTACK_PORT`, `LOGSTACK_DATA_DIR`).

# Access and operations

## Running and deploying

A systemd user unit runs the installed binary; its environment sets the port and the data folder, and it listens on loopback. `./deploy.sh` builds `cmd/logstack`, installs the binary where the unit's `ExecStart` expects it and restarts the unit; never build into place by hand.

# Working in this repo

## Tests

`go test ./...`. `cmd/openclaw-logpush` pushed the openclaw harness's session logs here; that harness is retired and nothing runs the pusher.
