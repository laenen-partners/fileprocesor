# FileProcessor

Pure Go library for file processing with pluggable backends (antivirus, PDF conversion, markdown extraction, thumbnails) and optional DBOS durable workflows.

## Quick reference

- **Language:** Go 1.25+
- **Workflow engine:** DBOS (optional, for durable async pipelines)
- **Dependencies:** ObjectStore (sibling module), Gotenberg, Docling, ClamAV, pdf2img
- **Jobs SDK:** `github.com/laenen-partners/jobs` (optional, wraps EntityStore for job tracking)
- **Task runner:** [Task](https://taskfile.dev) (`Taskfile.yml`)
- **Tool management:** [mise](https://mise.jdx.dev) (`mise.toml`)

## Project structure

```
antivirus/          ClamAV scanner integration
gotenberg/          Gotenberg PDF conversion / merge
docling/            Docling markdown extraction
pdf2img/            PDF-to-image thumbnail generation
config.go           Config + ConfigFromEnv()
types.go            Public types, enums, request/response structs
processor.go        NewProcessor constructor + public API methods + options
validate.go         Input validation (bucket allowlist, DAG checks)
workflow.go         Processor struct, DBOS workflow + operation executors
caller.go           Caller identity context propagation
docs/               Review reports
```

## Common commands

```sh
task build         # go build ./...
task vet           # go vet ./...
task test          # go test -v -count=1 ./...
task test:cover    # run tests with coverage summary
task tidy          # go mod tidy
task clean         # remove build artifacts and local data
```

## Usage

```go
// Sync-only (no database required)
proc, err := fileprocesor.NewProcessor(cfg, store)
scanResp, err := proc.ScanFile(ctx, fileprocesor.ScanFileRequest{Bucket: "b", Key: "k"})
pdfResp, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{...})

// With async Process workflow (requires DBOS + jobs client)
dbosCtx, _ := dbos.NewDBOSContext(ctx, dbos.Config{...})
jobsClient := jobs.NewClient(esClient)
proc, err := fileprocesor.NewProcessor(cfg, store,
    fileprocesor.WithDBOS(dbosCtx),
    fileprocesor.WithJobs(jobsClient),
)
dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)
dbos.Launch(dbosCtx)
defer dbos.Shutdown(dbosCtx, 30*time.Second)

resp, err := proc.Process(ctx, fileprocesor.ProcessInput{...})
job, err := proc.GetJob(ctx, resp.JobID)
```

## Configuration

`Config` struct fields (or use `ConfigFromEnv()` with environment variables):

| Variable | Default | Description |
|---|---|---|
| `GOTENBERG_URL` | | Gotenberg URL (empty = log stub) |
| `DOCLING_URL` | | Docling URL (empty = log stub) |
| `PDF2IMG_URL` | | pdf2img URL (empty = log stub) |
| `CLAMAV_ADDRESS` | | ClamAV TCP address (empty = skip scanning) |
| `ALLOWED_BUCKETS` | | Comma-separated bucket allowlist (empty = all allowed) |
| `MAX_FILE_SIZE_BYTES` | `268435456` | Max file size for downloads (256 MB) |

DBOS and jobs are configured via options, not Config — the consumer owns their lifecycle.

## Code conventions

- No `init()` functions; wire dependencies explicitly via `NewProcessor()`.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`.
- Use `slog` for structured logging.
- Backends with empty config URLs fall back to log-only stubs (no-op).
- Backend URLs are validated at startup (must be http/https with valid host).
- All inputs are validated before processing (bucket allowlist, key path safety, operation DAG integrity).
- `NewProcessor()` returns `(*Processor, error)`. DBOS and jobs are injected via `WithDBOS()` and `WithJobs()` options.
- **DBOS is optional:** Without `WithDBOS`, only sync methods are available. Process/GetJob/ListJobs/CancelJob return an error.
- **Consumer owns DBOS lifecycle:** Register workflows, launch, and shutdown are the consumer's responsibility.
- **Process is async:** Returns `JobID` + `WorkflowID` immediately; poll `GetJob` for status/progress/results.
- **Standalone methods are sync:** ScanFile, ConvertToPDF, MergePDFs, GenerateThumbnail, ExtractMarkdown return results directly.

## Security

- **Bucket allowlist:** `AllowedBuckets` restricts which buckets the processor can access.
- **Input validation:** All methods validate bucket/key, operation references, and reject path traversal.
- **Backend hardening:** HTTP client timeouts on all backends, `io.LimitReader` on responses.
