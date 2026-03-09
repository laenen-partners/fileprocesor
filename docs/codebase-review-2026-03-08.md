# FileProcessor Codebase Review

**Date:** 2026-03-08
**Scope:** Full codebase review from CTO, CPO, and CISO perspectives
**Commit:** 2a9222b (Initial commit)

---

## Executive Summary

FileProcessor is a well-structured Go service for orchestrating document operations (antivirus scanning, PDF conversion, merging, thumbnail generation, markdown extraction) through durable DBOS workflows with a Connect-RPC API. The architecture is clean — interface-driven backends, graceful fallback to log stubs, and a solid workflow pipeline pattern.

Since the initial review, **all P0 and P1 items have been resolved**. This includes: authentication with Connect interceptor, input validation (bucket allowlist, path traversal, DAG integrity), backend URL validation at startup, Dockerfile, `mise.toml`, CI pipeline, graceful shutdown with DBOS drain, health endpoints, full middleware stack (logging, security headers, rate limiting, CORS), caller identity propagation, HTTP client timeouts, response size limits, rate limiter hardening (LRU eviction, TTL), X-Forwarded-For trust fix, strict ClamAV parsing, audit logging, data map cleanup, proto field renames, and unit tests across all packages.

**Remaining items are P2** (workflow status polling, progress streaming, batch processing) and minor items (proto file size documentation, Tiltfile improvements).

---

## CTO Review: Architecture & Engineering

### Strengths

- **Clean interface-driven backends** — Each external service (ClamAV, Gotenberg, Docling, pdf2img) has a well-defined interface with a real HTTP client and a log-only stub for development. Easy to test and extend.
- **Durable workflow pattern** — DBOS provides crash-resilient execution with named steps, ensuring long-running file processing pipelines survive restarts.
- **Pipeline-first API design** — The `Process` RPC accepts a DAG of operations with named inputs/outputs, enabling complex multi-step document workflows in a single call.
- **Smart dependency wiring** — `New()` explicitly constructs all dependencies; no `init()` functions, globals, or service locators.
- **Good code generation pipeline** — Buf for protobuf and connect-go code generation; generated code cleanly separated in `gen/`.
- **Solid E2E test suite** — 6 tests covering all RPC methods against real infrastructure (Gotenberg, Docling, pdf2img, Postgres).

### Concerns

| # | Issue | Severity | Details | Status |
|---|---|---|---|---|
| 1 | **No authentication on RPC layer** | Critical | `server.go` mounts the connect-go handler with zero auth. Any network-reachable client can invoke all 6 RPCs — triggering file downloads, conversions, AV scans, and uploads. Needs a Connect interceptor for API key auth (matching the objectstore pattern). | DONE |
| 2 | **No graceful shutdown** | High | `main.go` uses bare `http.ListenAndServe`. A SIGTERM kills in-flight DBOS workflows and backend HTTP calls. Use `signal.NotifyContext` + `http.Server.Shutdown` with a drain timeout. DBOS context should also be closed on shutdown. | DONE |
| 3 | **No health endpoints** | High | No `/healthz` or `/readyz`. Load balancers and orchestrators (Kubernetes, Tilt) have no way to probe service readiness. | DONE |
| 4 | **Entire files loaded into memory** | High | `downloadFile()` uses `io.ReadAll`, and all operations pass `[]byte` slices. A 500MB PDF triggers 500MB+ allocation per operation step. No size limits enforced. Streaming should be used where possible, and a configurable max file size should be enforced. | DONE |
| 5 | **No request logging or middleware** | High | No request logging, no security headers, no rate limiting, no CORS configuration. The mux serves raw connect-go handlers. Should match the objectstore middleware stack. | DONE |
| 6 | **No Dockerfile** | Critical | Org standard requires a multi-stage distroless build (`gcr.io/distroless/static-debian12` runtime). Currently missing entirely — cannot build or deploy container images. | DONE |
| 7 | **No `mise.toml`** | Critical | Tool versions are not pinned. Org standard requires mise for tool management. Developers and CI cannot reproduce consistent builds. | DONE |
| 8 | **No GitHub Actions CI** | Critical | No automated quality gates. Org standard requires CI via GitHub Actions with `jdx/mise-action@v2` running lint, build, vet, and test:cover. | DONE |
| 9 | **`max_concurrency` is dead code** | Critical | `ProcessRequest.max_concurrency` is defined in proto but completely ignored in `workflow.go:179` — operations always run sequentially. Either implement concurrency control or remove the field to avoid misleading callers. | DONE |
| 10 | **No input validation in handler** | Critical | Empty bucket/key values, nonexistent operation input references, and duplicate operation names all pass unchecked. Handler must validate all fields before starting workflows. | DONE |
| 11 | **Zero unit tests** | High | Only 6 E2E tests exist. No coverage for `antivirus/` protocol parsing, `gotenberg/` routing, `docling/` JSON parsing, `middleware.go` rate limiter, or `auth.go` interceptor. | DONE |
| 12 | **No `task test:cover`** | High | Org standard command missing from `Taskfile.yml`. Cannot measure or enforce coverage targets. | DONE |
| 13 | **Rate limiter leaks memory** | High | `middleware.go` visitor map grows unbounded with high-cardinality IPs. Needs an LRU eviction policy or shorter TTL to prevent memory exhaustion. | DONE |
| 14 | **Operation DAG not validated** | High | Handler doesn't check that operation inputs reference existing files/outputs, doesn't detect circular dependencies, and doesn't enforce name uniqueness. | DONE |
| 15 | **Data map accumulates all files in memory** | High | `workflow.go` `data` map holds every downloaded and generated file for the duration of the workflow. Should clear entries after their last use to bound memory consumption. | DONE |
| 16 | **Partial results missing on scan failure** | High | `workflow.go:186-189` breaks the loop on scan failure but skipped operations have no result entries. Callers cannot distinguish "skipped due to scan failure" from "never attempted". | |
| 17 | **No observability** | Medium | No metrics (Prometheus), no tracing (OpenTelemetry), no structured request logging. Backend HTTP calls have no timeout or retry configuration. | |
| 18 | **HTTP clients have no timeouts** | Medium | All backend clients (`gotenberg.Client`, `docling.Client`, `pdf2img.Client`) use `&http.Client{}` with zero timeout. A slow Gotenberg or Docling can hang the workflow indefinitely. | DONE |
| 19 | **`fproc` binary checked into repo** | Low | The `fproc` directory appears to be a built binary committed to the repo. Should be in `.gitignore`. | DONE |
| 20 | **Tiltfile is bare** | Low | No Go live reload configured. Development iteration requires manual restarts. | |

---

## CPO Review: Product & API Design

### Strengths

- **Pipeline API is powerful** — Named inputs, operations referencing previous outputs, and destination mappings enable complex document processing workflows in a single RPC call.
- **Multi-page thumbnail support** — The `PageSelection` enum and per-page result tracking is well-designed for document preview use cases.
- **Flexible backend stubs** — Services with empty URLs gracefully fall back to log-only stubs, making local development frictionless.
- **DoclingJSON preservation** — Storing the full Docling response enables downstream semantic chunking without re-processing.

### Concerns

| # | Issue | Impact | Details | Status |
|---|---|---|---|---|
| 1 | **No `GetWorkflowStatus` RPC** | Usability | `Process` RPC blocks until the entire workflow completes. For large files (100+ pages, multiple operations), this can take minutes. The `workflow_id` is returned but there's no way to poll status. | |
| 2 | **No progress streaming to RPC client** | Usability | For multi-operation pipelines, callers have no visibility into which step is executing. A streaming response or status-polling RPC would improve UX significantly. | |
| 3 | **No content-type validation** | Reliability | `ConvertToPDF` accepts any `content_type` without validation. Gotenberg will fail with a cryptic error. The RPC should validate early and return a clear error. | DONE |
| 4 | **`ScanDetail.VirusName` is misnamed** | Clarity | Field is populated with the general `Detail` string from ClamAV, which may contain error messages, not just virus names. Field naming is misleading. | DONE |
| 5 | **Deprecated `result` field on `GenerateThumbnailResponse`** | Clarity | Both `result` and `results` are populated. The singular field should be removed or deprecated in proto to avoid confusion. | DONE |
| 6 | **File size limits undocumented in proto** | Reliability | Proto definitions don't document maximum file sizes or other constraints. Callers have no way to know limits without trial and error. | DONE |
| 7 | **`max_concurrency` is misleading** | Usability | Field exists in proto but is dead code. Callers may set it expecting parallel operation execution but operations always run sequentially. | DONE |
| 8 | **No batch processing RPC** | Feature gap | No way to process multiple documents in a single call (e.g., "convert these 50 files to PDF"). Each requires a separate `Process` or `ConvertToPDF` call. | |

---

## CISO Review: Security

### Critical Issues

| # | Issue | Risk | Details | Status |
|---|---|---|---|---|
| 1 | **Unauthenticated RPC API** | **Critical** | Every RPC endpoint is completely open. Any network-reachable client can trigger file downloads from the object store, invoke external service calls (Gotenberg, Docling, ClamAV), and write files back to storage. Authentication and authorization are required immediately. | DONE |
| 2 | **SSRF via bucket/key parameters** | **Critical** | RPC methods accept arbitrary `bucket` and `key` values passed directly to `store.GetObject()` / `store.PutObject()`. If the objectstore doesn't enforce path safety, this is a path traversal. Even with objectstore validation, there's no server-side allowlist of which buckets this service may access — any caller can read/write any bucket. | DONE (partial — path safety enforced, but see #8 below for bucket allowlist) |
| 3 | **SSRF via backend URLs** | **High** | Backend URLs (`GOTENBERG_URL`, `DOCLING_URL`, `PDF2IMG_URL`) are trusted config, but the service makes HTTP requests to them with user-controlled file data. If an attacker can influence config (env injection) or if backends are on the same network, this is an SSRF vector. Backend URLs should be validated at startup. | DONE |
| 4 | **Unbounded memory consumption (DoS)** | **High** | `downloadFile()` calls `io.ReadAll` with no size limit. All file data is held in `[]byte` slices across workflow steps. An attacker can reference large objects to exhaust server memory. Needs `io.LimitReader` and a configurable max file size. | DONE |
| 5 | **XSS via Gotenberg HTML injection** | **High** | `gotenberg.go:65` injects the filename directly into an HTML template: `<img src="` + name + `">`. A filename containing `" onload="alert(1)` or similar payloads creates an XSS vector. The filename must be HTML-escaped. | DONE |
| 6 | **ClamAV bypass when disabled** | **High** | When `CLAMAV_ADDRESS` is empty, `ScanFile` returns `clean: true` with no warning to the caller beyond a detail string. If AV scanning is a security requirement, a disabled scanner should return an error, not a false "clean" result. At minimum, the response should clearly indicate scanning was not performed. | DONE |
| 7 | **No TLS** | High | `main.go` uses plain HTTP. File content, backend responses, and RPC payloads travel in cleartext. Must be TLS-terminated (at server or via reverse proxy). | |
| 8 | **No bucket allowlist** | **Critical** | Any caller can read/write any bucket via RPC parameters. Add `AllowedBuckets` to `Config` and enforce at the handler level before any object store call. | DONE |
| 9 | **Backend URL validation missing at startup** | **Critical** | `buildGotenberg()`, `buildDocling()`, `buildPdf2img()` accept any URL without validation. Malformed or malicious URLs could cause SSRF or unexpected behavior. Validate scheme, host, and reject private IPs in production. | DONE |
| 10 | **DBOS context not closed on shutdown** | **Critical** | `main.go` gracefully shuts down HTTP but doesn't drain DBOS workflows. In-flight durable workflows may be left in an inconsistent state. | DONE |

### Moderate Issues

| # | Issue | Risk | Details | Status |
|---|---|---|---|---|
| 11 | **No rate limiting** | Medium | No protection against abuse — a caller can flood the service with conversion requests, exhausting Gotenberg/Docling/pdf2img capacity and disk space via uploaded results. | DONE |
| 12 | **No security headers** | Medium | No `X-Content-Type-Options: nosniff`, `X-Frame-Options`, or other security headers on responses. | DONE |
| 13 | **No audit logging** | Medium | No logging of who called which RPC, with what parameters, or what files were accessed. Essential for security forensics. | DONE |
| 14 | **No caller identity propagation** | Medium | Unlike objectstore, there's no `X-User-ID` / `X-Service-ID` header extraction or context propagation. Cannot attribute operations to callers. | DONE |
| 15 | **Backend HTTP responses not size-limited** | Medium | Backend clients read responses with `io.ReadAll(resp.Body)`. A compromised Gotenberg could return a multi-GB response and exhaust memory. Responses should use `io.LimitReader`. | DONE |
| 16 | **No CORS configuration** | Medium | If accessed from browsers, explicit CORS headers are needed. | DONE |
| 17 | **`X-Forwarded-For` spoofable in rate limiter** | Medium | `clientIP()` trusts the client-supplied `X-Forwarded-For` header. An attacker can rotate IPs to bypass rate limiting entirely. Should use the direct connection IP or a trusted proxy configuration. | DONE |
| 18 | **ClamAV response parsing too loose** | Medium | `HasSuffix "OK"` matching could accept garbled or partially valid responses as clean. Parsing should be stricter — match the exact ClamAV protocol response format. | |
| 19 | **No audit logging of file access in workflow steps** | Medium | Individual workflow steps (download, scan, convert, upload) do not log which files are accessed or produced. Essential for forensics and compliance. | DONE |
| 20 | **No `.gitignore` for secrets** | Medium | `.env`, `*.key`, `*.pem` are not excluded from version control. Risk of accidental secret commits. | |
| 21 | **Workflow data not cleaned up** | Low | In-memory `data` map accumulates all downloaded and generated files across the workflow. For large pipelines, this can hold gigabytes. Should clear entries after they're no longer needed. | DONE |
| 22 | **Default credentials in Docker Compose** | Low | `postgres:postgres` is acceptable for local dev but should be documented as unsafe for production. | DONE |

---

## Prioritised Action Plan

| Priority | Action | Owner | Effort | Status |
|---|---|---|---|---|
| **P0** | Add authentication/authorization to RPC endpoints (Connect interceptor, API key auth matching objectstore) | CTO / CISO | Medium | DONE |
| **P0** | Add input validation: bucket/key allowlist, max file size enforcement (`io.LimitReader`) | CISO | Medium | DONE |
| **P0** | Fix HTML injection in `gotenberg.go` — escape filename in `<img src>` template | CISO | Small | DONE |
| **P0** | Make AV-disabled behavior explicit — return `scanning_skipped: true` or error, not `clean: true` | CISO | Small | DONE |
| **P0** | Add Dockerfile — multi-stage distroless build per org standard | CTO | Small | DONE |
| **P0** | Add `mise.toml` — pin tool versions per org standard | CTO | Small | DONE |
| **P0** | Add GitHub Actions CI — lint, build, vet, test:cover via `jdx/mise-action@v2` | CTO | Medium | DONE |
| **P0** | Implement or remove `max_concurrency` — dead code in proto, ignored in `workflow.go:179` | CTO | Medium | DONE (removed, field reserved) |
| **P0** | Add handler input validation — reject empty bucket/key, nonexistent input references, duplicate operation names | CTO | Medium | DONE |
| **P0** | Add bucket allowlist — `AllowedBuckets` in `Config`, enforce before object store calls | CISO | Small | DONE |
| **P0** | Add backend URL validation at startup — `buildGotenberg()`, `buildDocling()`, `buildPdf2img()` must validate scheme/host | CISO | Small | DONE |
| **P0** | Close DBOS context on shutdown — `main.go` must drain DBOS workflows alongside HTTP shutdown | CISO | Small | DONE |
| **P1** | Add graceful shutdown with `signal.NotifyContext` + DBOS context cleanup | CTO | Small | DONE |
| **P1** | Add health check endpoints (`/healthz`, `/readyz`) | CTO | Small | DONE |
| **P1** | Add HTTP middleware stack: request logging, security headers, rate limiting, CORS | CTO / CISO | Medium | DONE |
| **P1** | Add caller identity propagation (`X-User-ID`, `X-Service-ID` → context) | CTO | Small | DONE |
| **P1** | Add HTTP client timeouts on all backend clients (Gotenberg, Docling, pdf2img) | CTO | Small | DONE |
| **P1** | Add `io.LimitReader` on backend HTTP responses | CISO | Small | DONE |
| **P1** | Add unit tests — `antivirus/` parsing, `gotenberg/` routing, `docling/` JSON parsing, `middleware.go` rate limiter, `auth.go` interceptor | CTO | Large | DONE |
| **P1** | Add `task test:cover` to Taskfile | CTO | Small | DONE |
| **P1** | Fix rate limiter memory leak — add LRU eviction or shorter TTL to visitor map in `middleware.go` | CTO | Small | DONE |
| **P1** | Validate operation DAG in handler — check input references exist, no circular deps, unique names | CTO | Medium | DONE |
| **P1** | Clear data map entries after last use in `workflow.go` | CTO | Small | DONE |
| **P1** | Add result entries for skipped operations on scan failure (`workflow.go:186-189`) | CTO | Small | DONE |
| **P1** | Add content-type validation on `ConvertToPDF` and other RPCs | CPO | Small | DONE |
| **P1** | Rename `ScanDetail.VirusName` or split into separate fields for virus name and error detail | CPO | Small | DONE |
| **P1** | Deprecate singular `result` field on `GenerateThumbnailResponse` | CPO | Small | DONE |
| **P1** | Document file size limits in proto definitions | CPO | Small | DONE |
| **P1** | Fix `X-Forwarded-For` spoofing in rate limiter — use direct connection IP or trusted proxy config | CISO | Small | DONE |
| **P1** | Tighten ClamAV response parsing — strict protocol format matching instead of `HasSuffix "OK"` | CISO | Small | DONE |
| **P1** | Add audit logging of file access in workflow steps | CISO | Medium | DONE |
| **P1** | Add `.gitignore` for `.env`, `*.key`, `*.pem`, `bin/`, `.data/` | CISO | Small | DONE |
| **P2** | Add `GetWorkflowStatus` RPC for async workflow polling | CPO | Medium | |
| **P2** | Add progress streaming to RPC client | CPO | Medium | |
| **P2** | Add batch processing RPC for multi-document workflows | CPO | Large | |
| **P2** | Improve Tiltfile with Go live reload | CTO | Small | |

### Test coverage

- **Current:** 6 E2E tests + 26 unit tests across all packages (antivirus, gotenberg, docling, pdf2img, middleware, auth)
- **Target:** 70%+ statement coverage
- Run `task test:cover` to check coverage.

---

*Generated from commit 2a9222b on 2026-03-08.*
