# FileProcessor

A pure Go library for file processing with pluggable backends — antivirus scanning, PDF conversion, merging, thumbnail generation, and markdown extraction. Optionally backed by [DBOS](https://docs.dbos.dev) durable workflows and [jobs](https://github.com/laenen-partners/jobs) for async pipeline execution.

## Features

- **Antivirus scanning** — ClamAV integration via the INSTREAM protocol
- **PDF conversion** — Any document to PDF via Gotenberg (LibreOffice + Chromium)
- **PDF merging** — Combine multiple PDFs into one
- **Thumbnail generation** — Page thumbnails from PDFs (JPEG, PNG, or WebP)
- **Markdown extraction** — PDF to Markdown/HTML via Docling
- **Durable workflows** — Optional DBOS integration for crash-resilient async pipelines
- **Job tracking** — Optional jobs SDK integration for progress monitoring

## Installation

```sh
go get github.com/laenen-partners/fileprocesor
```

## Quick start

### Sync usage (no database required)

```go
import (
    "github.com/laenen-partners/fileprocesor"
    "github.com/laenen-partners/objectstore"
)

cfg := fileprocesor.Config{
    GotenbergURL: "http://localhost:3000",
    DoclingURL:   "http://localhost:5001",
    PDF2ImgURL:   "http://localhost:5002",
    ClamAVAddr:   "localhost:3310",
}

store := objectstore.New(/* ... */)
proc, err := fileprocesor.NewProcessor(cfg, store)

// Scan a file
scanResp, err := proc.ScanFile(ctx, fileprocesor.ScanFileRequest{
    Bucket: "uploads",
    Key:    "document.pdf",
})

// Convert to PDF
pdfResp, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{
    Bucket:      "uploads",
    Key:         "report.docx",
    ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    Destination: &fileprocesor.FileRef{
        Bucket: "processed", Key: "report.pdf", ContentType: "application/pdf",
    },
})
```

### Async pipeline (requires DBOS + jobs)

```go
import (
    "github.com/dbos-inc/dbos-transact-golang/dbos"
    "github.com/laenen-partners/jobs"
)

dbosCtx, _ := dbos.NewDBOSContext(ctx, dbos.Config{/* ... */})
jobsClient := jobs.NewClient(esClient)

proc, err := fileprocesor.NewProcessor(cfg, store,
    fileprocesor.WithDBOS(dbosCtx),
    fileprocesor.WithJobs(jobsClient),
)

dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)
dbos.Launch(dbosCtx)
defer dbos.Shutdown(dbosCtx, 30*time.Second)

// Submit a processing pipeline (returns immediately)
resp, err := proc.Process(ctx, fileprocesor.ProcessInput{
    Inputs: []fileprocesor.FileInput{
        {Name: "doc", Bucket: "uploads", Key: "contract.docx", ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
    },
    Operations: []fileprocesor.Operation{
        {Name: "scan",  Inputs: []string{"doc"}, Scan: &fileprocesor.ScanOp{}},
        {Name: "pdf",   Inputs: []string{"doc"}, ConvertToPDF: &fileprocesor.ConvertToPDFOp{}},
        {Name: "thumb", Inputs: []string{"pdf"}, Thumbnail: &fileprocesor.ThumbnailOp{Width: 400, DPI: 150}},
        {Name: "text",  Inputs: []string{"pdf"}, ExtractMarkdown: &fileprocesor.ExtractMarkdownOp{}},
    },
    Destinations: map[string]fileprocesor.FileRef{
        "pdf":   {Bucket: "processed", Key: "contract.pdf", ContentType: "application/pdf"},
        "thumb": {Bucket: "processed", Key: "contract-thumb.jpg", ContentType: "image/jpeg"},
    },
})

// Poll for results
job, err := proc.GetJob(ctx, resp.JobID)
```

## API

### Sync methods

| Method | Description |
|---|---|
| `ScanFile` | Scan a file for viruses |
| `ConvertToPDF` | Convert a document to PDF |
| `MergePDFs` | Merge multiple PDFs into one |
| `GenerateThumbnail` | Generate thumbnails from a PDF |
| `ExtractMarkdown` | Extract Markdown and HTML from a PDF |

### Async methods (require DBOS + jobs)

| Method | Description |
|---|---|
| `Process` | Submit a pipeline of operations (returns job ID) |
| `GetJob` | Get the current state of a processing job |
| `ListJobs` | List jobs matching filters |
| `CancelJob` | Cancel a running job |

## Configuration

Use `Config` struct fields directly or `ConfigFromEnv()` to read from environment variables:

| Variable | Default | Description |
|---|---|---|
| `GOTENBERG_URL` | | Gotenberg endpoint (empty = log-only stub) |
| `DOCLING_URL` | | Docling endpoint (empty = log-only stub) |
| `PDF2IMG_URL` | | pdf2img endpoint (empty = log-only stub) |
| `CLAMAV_ADDRESS` | | ClamAV TCP address (empty = scanning disabled) |
| `ALLOWED_BUCKETS` | | Comma-separated bucket allowlist (empty = all allowed) |
| `MAX_FILE_SIZE_BYTES` | `268435456` | Max file size for downloads (256 MB) |

Backends with empty URLs fall back to log-only stubs. DBOS and jobs are configured via `WithDBOS()` and `WithJobs()` options — the consumer owns their lifecycle.

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
workflow.go         DBOS workflow + operation executors
caller.go           Caller identity context propagation
```

## Development

### Prerequisites

- [Go](https://go.dev) 1.25+
- [Task](https://taskfile.dev) (task runner)
- [mise](https://mise.jdx.dev) (tool management)
- [Docker](https://docs.docker.com/get-docker/) (for backend services)

### Setup

```sh
mise install
task test
```

### Available tasks

```sh
task build          # go build ./...
task vet            # go vet ./...
task test           # go test -v -count=1 ./...
task test:cover     # run tests with coverage summary
task test:e2e       # run e2e tests (requires infra)
task tidy           # go mod tidy
task clean          # remove build artifacts and local data
task infra:up       # start infrastructure via Tilt
task infra:down     # stop infrastructure
```

### Running tests

Unit tests run without infrastructure:

```sh
task test
```

End-to-end tests require backend services:

```sh
task infra:up
task test:e2e
```

## Architecture

```
                  +-----------+
  Consumer ------>| Processor |----> Sync methods (ScanFile, ConvertToPDF, ...)
                  +-----------+
                       |
                       v (optional)
                  +-----------+
                  |   DBOS    |----> Process workflow (async pipeline)
                  +-----------+
                  /    |    |   \
                 v     v    v    v
            Scanner  Gotenberg  Docling  pdf2img
           (ClamAV)   (PDF)     (MD)     (JPEG)
                 \     |    |    /
                  v     v    v  v
                  +------------+
                  | ObjectStore|----> Local filesystem or S3
                  +------------+
```

Files are read from and written to the [ObjectStore](https://github.com/laenen-partners/objectstore). The `Process` method downloads inputs, runs operations sequentially as DBOS steps, and uploads results to the specified destinations.

## License

Proprietary. All rights reserved.
