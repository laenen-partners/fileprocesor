# FileProcessor

A file processing microservice that orchestrates document operations through durable workflows. Upload a file and run a pipeline of operations — antivirus scanning, PDF conversion, merging, thumbnail generation, and markdown extraction — all through a single API call.

Built with [Connect-RPC](https://connectrpc.com) and [DBOS](https://docs.dbos.dev) for reliable, resumable execution.

## Features

- **Antivirus scanning** — ClamAV integration via the INSTREAM protocol
- **PDF conversion** — Any document to PDF via Gotenberg (LibreOffice + Chromium)
- **PDF merging** — Combine multiple PDFs into one
- **Thumbnail generation** — First-page JPEG thumbnails from PDFs
- **Markdown extraction** — PDF to Markdown/HTML via Docling
- **Durable workflows** — DBOS ensures operations survive crashes and restarts
- **Pipeline API** — Chain operations in a single `Process` RPC call

## Prerequisites

- [Go](https://go.dev) 1.25+
- [Task](https://taskfile.dev) (task runner)
- [Docker](https://docs.docker.com/get-docker/) and [Docker Compose](https://docs.docker.com/compose/)
- [Buf](https://buf.build) (protobuf tooling, only needed for code generation)
- [Tilt](https://tilt.dev) (optional, for `task infra:up`)

## Quick start

**1. Clone and configure**

```sh
git clone https://github.com/laenen-partners/fileprocesor.git
cd fileprocesor
cp .env.sample .env
```

**2. Start infrastructure**

```sh
task infra:up
```

This launches PostgreSQL, Gotenberg, Docling, pdf2img, and ClamAV via Docker Compose.

**3. Run the server**

```sh
task run
```

The server starts on `http://localhost:3001` by default.

**4. Make a request**

```sh
# Convert a file to PDF (assumes the file is already in the object store)
buf curl --schema proto \
  --data '{
    "bucket": "my-bucket",
    "key": "report.docx",
    "content_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "destination": {
      "bucket": "my-bucket",
      "key": "report.pdf",
      "content_type": "application/pdf"
    }
  }' \
  http://localhost:3001/fileprocessor.v1.FileProcessorService/ConvertToPDF
```

## API

All RPCs are defined in [`proto/fileprocessor/v1/fileprocessor.proto`](proto/fileprocessor/v1/fileprocessor.proto).

### Standalone RPCs

| RPC | Description |
|---|---|
| `ScanFile` | Scan a file for viruses |
| `ConvertToPDF` | Convert a document to PDF |
| `MergePDFs` | Merge multiple PDFs into one |
| `GenerateThumbnail` | Generate a JPEG thumbnail from a PDF |
| `ExtractMarkdown` | Extract Markdown and HTML from a PDF |

### Pipeline RPC

`Process` runs a sequence of operations as a durable DBOS workflow:

```sh
buf curl --schema proto \
  --data '{
    "inputs": [
      {"name": "doc", "bucket": "uploads", "key": "contract.docx", "content_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}
    ],
    "operations": [
      {"name": "scan",    "inputs": ["doc"],     "scan": {}},
      {"name": "pdf",     "inputs": ["doc"],     "convert_to_pdf": {}},
      {"name": "thumb",   "inputs": ["pdf"],     "thumbnail": {"width": 400, "dpi": 150}},
      {"name": "text",    "inputs": ["pdf"],     "extract_markdown": {}}
    ],
    "destinations": {
      "pdf":   {"bucket": "processed", "key": "contract.pdf",       "content_type": "application/pdf"},
      "thumb": {"bucket": "processed", "key": "contract-thumb.jpg", "content_type": "image/jpeg"}
    }
  }' \
  http://localhost:3001/fileprocessor.v1.FileProcessorService/Process
```

Operations run in order. If a scan detects a virus, the workflow stops immediately.

## Configuration

Copy `.env.sample` to `.env` and adjust:

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3001` | Server listen address |
| `DBOS_DATABASE_URL` | | PostgreSQL connection string (required) |
| `OBJECT_STORE` | `file` | Storage backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | Local storage directory |
| `OBJECT_STORE_URL` | | Public base URL for presigned URLs |
| `OBJECT_STORE_SECRET` | | HMAC signing secret for presigned URLs |
| `GOTENBERG_URL` | | Gotenberg endpoint (empty = log stub) |
| `DOCLING_URL` | | Docling endpoint (empty = log stub) |
| `PDF2IMG_URL` | | pdf2img endpoint (empty = log stub) |
| `CLAMAV_ADDRESS` | | ClamAV TCP address (empty = scanning disabled) |

When a service URL is left empty, the corresponding operation uses a log-only stub. When `CLAMAV_ADDRESS` is empty, antivirus scanning is disabled and a warning is logged at startup.

## Project structure

```
cmd/fproc/              Server binary entry point
proto/                  Protobuf service definitions
gen/                    Generated code (do not edit)
antivirus/              ClamAV scanner integration
gotenberg/              Gotenberg PDF conversion and merging
docling/                Docling markdown/HTML extraction
pdf2img/                PDF-to-image thumbnail generation
config.go               Environment-based configuration
server.go               Service wiring and initialization
handler.go              Connect-RPC request handlers
workflow.go             DBOS workflow and operation executors
e2e_test.go             End-to-end integration tests
docker-compose.yml      Infrastructure services
Taskfile.yml            Task runner commands
```

## Development

### Available tasks

```sh
task                # List all tasks
task build          # Build binary to bin/fproc
task run            # Run the server
task test           # Run all tests
task test:e2e       # Run e2e tests (requires infra)
task generate       # Regenerate protobuf code
task lint           # Lint protobuf definitions
task vet            # Run go vet
task tidy           # Tidy go modules
task infra:up       # Start infrastructure via Tilt
task infra:down     # Stop infrastructure and remove volumes
```

### Running tests

Unit tests run without any infrastructure:

```sh
task test
```

End-to-end tests require the full Docker Compose stack:

```sh
task infra:up
task test:e2e
```

The e2e tests exercise every RPC against real Gotenberg, Docling, and pdf2img services. ClamAV is optional — tests skip antivirus and verify the disabled-scan path instead.

### Regenerating protobuf code

```sh
task generate
```

This runs `buf generate` and outputs Go + Connect code to `gen/`. Never edit files in `gen/` manually.

## Architecture

```
                    +-----------+
  Client ----RPC--->| Handler   |----> Standalone RPCs (ScanFile, ConvertToPDF, ...)
                    +-----------+
                         |
                         v
                    +-----------+
                    | Processor |----> DBOS Workflow (Process RPC)
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

Files are read from and written to the [ObjectStore](https://github.com/laenen-partners/objectstore) service. The `Process` RPC downloads inputs, runs operations sequentially as DBOS steps, and uploads results to the specified destinations.

## License

Proprietary. All rights reserved.
