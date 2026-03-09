# FileProcessor

Go library and server for file processing with Connect-RPC API, DBOS durable workflows, and pluggable backends (antivirus, PDF conversion, markdown extraction, thumbnails).

## Quick reference

- **Language:** Go 1.25+
- **RPC framework:** Connect-RPC (protobuf)
- **Workflow engine:** DBOS (durable execution)
- **Dependencies:** ObjectStore (sibling module), Gotenberg, Docling, ClamAV, pdf2img
- **Jobs SDK:** `github.com/laenen-partners/jobs` (wraps EntityStore for job tracking)
- **Task runner:** [Task](https://taskfile.dev) (`Taskfile.yml`)
- **Proto tool:** [Buf](https://buf.build) (`buf.yaml`, `buf.gen.yaml`)
- **Tool management:** [mise](https://mise.jdx.dev) (`mise.toml`)

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
server.go           New() wires everything, returns (handler, closer, error)
handler.go          Connect-RPC handler (individual RPCs)
validate.go         Input validation (bucket allowlist, DAG checks)
workflow.go         Processor, DBOS workflow + operation executors
auth.go             Connect-RPC auth interceptor (API key, Bearer token)
caller.go           Caller identity context propagation
middleware.go       HTTP middleware (logging, security headers, rate limiting, CORS)
docker-compose.yml  Infrastructure (Postgres, ClamAV, Gotenberg, Docling, pdf2img)
Dockerfile          Multi-stage distroless build
Tiltfile            Tilt dev environment
docs/               Review reports
```

## Common commands

```sh
task generate      # buf generate (proto -> Go)
task generate:apikey # generate a random API key
task lint          # buf lint
task build         # go build -o bin/fproc ./cmd/fproc
task vet           # go vet ./...
task test          # go test -v -count=1 ./...
task test:cover    # run tests with coverage summary
task test:e2e      # run E2E tests (requires task infra:up)
task tidy          # go mod tidy
task clean         # remove build artifacts and local data
task run           # run the server locally
task infra:up      # start infrastructure via Tilt
task infra:down    # stop infrastructure
```

## Configuration

Set the following environment variables (or use a `.env` file loaded by mise):

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3001` | Server listen address |
| `DBOS_DATABASE_URL` | | PostgreSQL connection string (required) |
| `GOTENBERG_URL` | | Gotenberg URL (empty = log stub) |
| `DOCLING_URL` | | Docling URL (empty = log stub) |
| `PDF2IMG_URL` | | pdf2img URL (empty = log stub) |
| `CLAMAV_ADDRESS` | | ClamAV TCP address (empty = skip scanning) |
| `ENTITY_STORE_URL` | | EntityStore URL for job tracking (empty = disabled) |
| `API_KEYS` | | Comma-separated API keys for RPC auth (empty = auth disabled) |
| `ALLOWED_BUCKETS` | | Comma-separated bucket allowlist (empty = all allowed) |
| `RATE_LIMIT` | `0` | Requests per second per IP (0 = disabled) |
| `RATE_BURST` | `20` | Burst allowance per IP |
| `CORS_ORIGINS` | | Comma-separated allowed CORS origins |
| `MAX_FILE_SIZE_BYTES` | `268435456` | Max file size for downloads (256 MB) |
| `OBJECT_STORE` | `file` | ObjectStore backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | | File backend: public base URL |

## Code conventions

- No `init()` functions; wire dependencies explicitly in `main.go` or `New()`.
- Generated code lives in `gen/` -- never edit manually, regenerate with `task generate`.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`.
- Use `slog` for structured logging.
- Backends with empty config URLs fall back to log-only stubs (no-op).
- Backend URLs are validated at startup (must be http/https with valid host).
- DBOS workflows ensure durable execution; each operation runs as a named step.
- All RPC inputs are validated before processing (bucket allowlist, key path safety, operation DAG integrity).
- `server.New()` returns `(http.Handler, Closer, error)` — the Closer must be called on shutdown to drain DBOS workflows.

## Security

- **Authentication:** Connect-RPC interceptor with API key auth (`Authorization: Bearer <key>`), constant-time comparison.
- **Bucket allowlist:** `ALLOWED_BUCKETS` restricts which buckets the service can access.
- **Input validation:** All RPCs validate bucket/key, operation references, and reject path traversal.
- **Rate limiting:** Per-IP token bucket with LRU eviction (max 100k visitors, 3-min TTL).
- **Security headers:** `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, etc.
- **Backend hardening:** HTTP client timeouts on all backends, `io.LimitReader` on responses.
- **Graceful shutdown:** `signal.NotifyContext` + 30s HTTP drain + DBOS workflow drain.
