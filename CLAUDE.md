# FileProcessor

Go library and server for file processing with Connect-RPC API, DBOS durable workflows, and pluggable backends (antivirus, PDF conversion, markdown extraction, thumbnails).

## Quick reference

- **Language:** Go 1.25+
- **RPC framework:** Connect-RPC (protobuf)
- **Workflow engine:** DBOS (durable execution)
- **Dependencies:** ObjectStore (sibling module), Gotenberg, Docling, ClamAV, pdf2img
- **Task runner:** [Task](https://taskfile.dev) (`Taskfile.yml`)
- **Proto tool:** [Buf](https://buf.build) (`buf.yaml`, `buf.gen.yaml`)

## Project structure

```
cmd/fproc/          Server binary
proto/              Protobuf definitions
gen/                Generated protobuf/connect code (do not edit)
antivirus/          ClamAV scanner integration
gotenberg/          Gotenberg PDF conversion / merge
docling/            Docling markdown extraction
pdf2img/            PDF-to-image thumbnail generation
config.go           Config + ConfigFromEnv()
server.go           New() wires everything together
handler.go          Connect-RPC handler (individual RPCs)
workflow.go         Processor, DBOS workflow + operation executors
docker-compose.yml  Infrastructure (Postgres, ClamAV, Gotenberg, Docling, pdf2img)
Tiltfile            Tilt dev environment
```

## Common commands

```sh
task generate   # buf generate (proto -> Go)
task lint       # buf lint
task build      # go build ./...
task vet        # go vet ./...
task test       # go test -v -count=1 ./...
task tidy       # go mod tidy
task infra:up   # start infrastructure via Tilt
task infra:down # stop infrastructure
```

## Configuration

Set the following environment variables (or use a `.env` file):

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3001` | Server listen address |
| `DBOS_DATABASE_URL` | | PostgreSQL connection string (required) |
| `GOTENBERG_URL` | | Gotenberg URL (empty = log stub) |
| `DOCLING_URL` | | Docling URL (empty = log stub) |
| `PDF2IMG_URL` | | pdf2img URL (empty = log stub) |
| `CLAMAV_ADDRESS` | | ClamAV TCP address (empty = skip scanning) |
| `OBJECT_STORE` | `file` | ObjectStore backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | | File backend: public base URL |
| `OBJECT_STORE_SECRET` | | File backend: HMAC signing secret |

## Code conventions

- No `init()` functions; wire dependencies explicitly in `main.go` or `New()`.
- Generated code lives in `gen/` -- never edit manually, regenerate with `task generate`.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`.
- Use `slog` for structured logging.
- Backends with empty config URLs fall back to log-only stubs (no-op).
- DBOS workflows ensure durable execution; each operation runs as a named step.
