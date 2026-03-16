package fileprocesor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/laenen-partners/fileprocesor/antivirus"
	"github.com/laenen-partners/fileprocesor/docling"
	"github.com/laenen-partners/fileprocesor/gotenberg"
	"github.com/laenen-partners/fileprocesor/pdf2img"
	"github.com/laenen-partners/jobs"
	"github.com/laenen-partners/objectstore"
)

// Option configures optional Processor capabilities.
type Option func(*Processor)

// WithDBOS enables durable workflow execution via DBOS.
// The caller owns the DBOSContext lifecycle (launch, shutdown).
// Required for Process; without it only sync methods are available.
func WithDBOS(ctx dbos.DBOSContext) Option {
	return func(p *Processor) {
		p.dbosCtx = ctx
	}
}

// WithJobs enables job tracking for the Process workflow.
// Requires WithDBOS. Without it, Process returns an error.
func WithJobs(client *jobs.Client) Option {
	return func(p *Processor) {
		p.jobs = client
	}
}

// NewProcessor creates a file processor.
//
// By default only sync methods (ScanFile, ConvertToPDF, MergePDFs,
// GenerateThumbnail, ExtractMarkdown) are available.
//
// To enable the async Process workflow, pass WithDBOS and WithJobs.
// The caller must register the workflow and manage the DBOS lifecycle:
//
//	dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)
//	dbos.Launch(dbosCtx)
//	defer dbos.Shutdown(dbosCtx, 30*time.Second)
func NewProcessor(cfg Config, store objectstore.Store, opts ...Option) (*Processor, error) {
	// Validate backend URLs.
	for name, rawURL := range map[string]string{
		"GOTENBERG_URL": cfg.GotenbergURL,
		"DOCLING_URL":   cfg.DoclingURL,
		"PDF2IMG_URL":   cfg.PDF2ImgURL,
	} {
		if rawURL != "" {
			if err := validateBackendURL(name, rawURL); err != nil {
				return nil, err
			}
		}
	}

	maxFileSize := cfg.MaxFileSizeBytes
	if maxFileSize <= 0 {
		maxFileSize = 256 << 20 // 256 MB default
	}

	// Build bucket allowlist set.
	var allowedBuckets map[string]bool
	if len(cfg.AllowedBuckets) > 0 {
		allowedBuckets = make(map[string]bool, len(cfg.AllowedBuckets))
		for _, b := range cfg.AllowedBuckets {
			allowedBuckets[b] = true
		}
	}

	proc := &Processor{
		store:          store,
		scanner:        buildScanner(cfg),
		gotenberg:      buildGotenberg(cfg),
		docling:        buildDocling(cfg),
		pdf2img:        buildPdf2img(cfg),
		maxFileSize:    maxFileSize,
		allowedBuckets: allowedBuckets,
	}

	for _, opt := range opts {
		opt(proc)
	}

	return proc, nil
}

func buildScanner(cfg Config) antivirus.Scanner {
	if cfg.ClamAVAddr == "" {
		slog.Warn("antivirus scanning disabled: CLAMAV_ADDRESS not set")
		return nil
	}
	return antivirus.NewClamAVScanner(cfg.ClamAVAddr)
}

func buildGotenberg(cfg Config) gotenberg.Converter {
	if cfg.GotenbergURL == "" {
		return gotenberg.NewLogConverter()
	}
	return gotenberg.New(cfg.GotenbergURL)
}

func buildDocling(cfg Config) docling.Converter {
	if cfg.DoclingURL == "" {
		return docling.NewLogConverter()
	}
	return docling.New(cfg.DoclingURL)
}

func buildPdf2img(cfg Config) pdf2img.Converter {
	if cfg.PDF2ImgURL == "" {
		return pdf2img.NewLogConverter()
	}
	return pdf2img.New(cfg.PDF2ImgURL)
}

// --- Public methods ---

// Process submits a processing pipeline and returns immediately with a job ID.
// Requires DatabaseURL and EntityStoreURL to be configured.
func (p *Processor) Process(ctx context.Context, input ProcessInput) (*ProcessResponse, error) {
	if p.dbosCtx == nil {
		return nil, fmt.Errorf("async processing not available: DatabaseURL is required")
	}
	if p.jobs == nil {
		return nil, fmt.Errorf("job tracking not configured: ENTITY_STORE_URL is required")
	}

	if err := p.validateProcessInput(input); err != nil {
		return nil, err
	}

	// Propagate caller identity as a tag.
	caller := CallerFromContext(ctx)
	if caller.UserID != "" {
		input.Tags = append(input.Tags, "user:"+caller.UserID)
	}

	wfID := uuid.NewString()
	handle, err := dbos.RunWorkflow(p.dbosCtx, p.ProcessWorkflow, input,
		dbos.WithWorkflowID(wfID))
	if err != nil {
		return nil, fmt.Errorf("start workflow: %w", err)
	}

	// Wait for the job entity ID to be published by the workflow.
	jobID, err := dbos.GetEvent[string](p.dbosCtx, handle.GetWorkflowID(), "job_entity_id", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("waiting for job creation: %w", err)
	}

	return &ProcessResponse{
		JobID:             jobID,
		ExternalReference: wfID,
	}, nil
}

// GetJob returns the current state of a processing job.
func (p *Processor) GetJob(ctx context.Context, jobID string) (*JobInfo, error) {
	if p.jobs == nil {
		return nil, fmt.Errorf("job tracking not configured")
	}
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	job, err := p.jobs.GetJob(ctx, jobID)
	if err != nil {
		if err == jobs.ErrNotFound {
			return nil, fmt.Errorf("job not found")
		}
		return nil, fmt.Errorf("get job: %w", err)
	}

	return jobToInfo(job), nil
}

// ListJobs returns jobs matching the given filters.
func (p *Processor) ListJobs(ctx context.Context, filter ListJobsFilter) ([]JobInfo, error) {
	if p.jobs == nil {
		return nil, fmt.Errorf("job tracking not configured")
	}

	tags := append([]string{"file_processing"}, filter.Tags...)
	jobList, err := p.jobs.ListJobs(ctx, jobs.ListFilter{
		Tags:   tags,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	result := make([]JobInfo, 0, len(jobList))
	for _, j := range jobList {
		result = append(result, *jobToInfo(&j))
	}
	return result, nil
}

// CancelJob marks a running job as cancelled.
func (p *Processor) CancelJob(ctx context.Context, jobID string) error {
	if p.jobs == nil {
		return fmt.Errorf("job tracking not configured")
	}
	if jobID == "" {
		return fmt.Errorf("job_id is required")
	}

	if err := p.jobs.CancelJob(ctx, jobID); err != nil {
		if err == jobs.ErrNotFound {
			return fmt.Errorf("job not found")
		}
		if err == jobs.ErrAlreadyFinalized {
			return fmt.Errorf("job already finalized")
		}
		return fmt.Errorf("cancel job: %w", err)
	}
	return nil
}

// ScanFile scans a file for viruses.
func (p *Processor) ScanFile(ctx context.Context, req ScanFileRequest) (*ScanFileResponse, error) {
	if err := p.validateFileRef(req.Bucket, req.Key); err != nil {
		return nil, err
	}
	if p.scanner == nil {
		return &ScanFileResponse{Clean: false, Detail: "scanning disabled: no antivirus configured"}, nil
	}

	data, err := p.downloadFile(ctx, req.Bucket, req.Key)
	if err != nil {
		return nil, err
	}
	result, err := p.scanner.Scan(ctx, data)
	if err != nil {
		return nil, err
	}
	return &ScanFileResponse{
		Clean:  result.Clean,
		Detail: result.Detail,
	}, nil
}

// ConvertToPDF converts a file to PDF.
func (p *Processor) ConvertToPDF(ctx context.Context, req ConvertToPDFRequest) (*ConvertToPDFResponse, error) {
	if err := p.validateFileRef(req.Bucket, req.Key); err != nil {
		return nil, err
	}
	if req.ContentType == "" {
		return nil, fmt.Errorf("content_type is required for conversion routing")
	}
	if err := p.validateFileRefPtr(req.Destination, "destination"); err != nil {
		return nil, err
	}
	data, err := p.downloadFile(ctx, req.Bucket, req.Key)
	if err != nil {
		return nil, err
	}
	pdfBytes, err := p.gotenberg.ConvertToPDF(ctx, req.Key, data, req.ContentType)
	if err != nil {
		return nil, err
	}

	resp := &ConvertToPDFResponse{SizeBytes: int64(len(pdfBytes))}
	if dest := req.Destination; dest != nil {
		if err := p.uploadFile(ctx, dest.Bucket, dest.Key, "application/pdf", pdfBytes); err != nil {
			return nil, err
		}
		resp.Destination = dest
	}
	return resp, nil
}

// MergePDFs merges multiple PDF files into one.
func (p *Processor) MergePDFs(ctx context.Context, req MergePDFsRequest) (*MergePDFsResponse, error) {
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("at least one file is required")
	}
	for _, f := range req.Files {
		if err := p.validateFileRef(f.Bucket, f.Key); err != nil {
			return nil, err
		}
	}
	if err := p.validateFileRefPtr(req.Destination, "destination"); err != nil {
		return nil, err
	}
	pdfs := make([]gotenberg.NamedPDF, 0, len(req.Files))
	for _, f := range req.Files {
		data, err := p.downloadFile(ctx, f.Bucket, f.Key)
		if err != nil {
			return nil, err
		}
		pdfs = append(pdfs, gotenberg.NamedPDF{Name: f.Key, Data: data})
	}
	merged, err := p.gotenberg.MergePDFs(ctx, pdfs)
	if err != nil {
		return nil, err
	}

	resp := &MergePDFsResponse{SizeBytes: int64(len(merged))}
	if dest := req.Destination; dest != nil {
		if err := p.uploadFile(ctx, dest.Bucket, dest.Key, "application/pdf", merged); err != nil {
			return nil, err
		}
		resp.Destination = dest
	}
	return resp, nil
}

// GenerateThumbnail generates thumbnail images from a PDF.
func (p *Processor) GenerateThumbnail(ctx context.Context, req GenerateThumbnailRequest) (*GenerateThumbnailResponse, error) {
	if err := p.validateFileRef(req.Bucket, req.Key); err != nil {
		return nil, err
	}
	if err := p.validateFileRefPtr(req.Destination, "destination"); err != nil {
		return nil, err
	}
	data, err := p.downloadFile(ctx, req.Bucket, req.Key)
	if err != nil {
		return nil, err
	}

	format := string(req.Format)
	if format == "" {
		format = "jpg"
	}
	mime := pdf2img.FormatToMIME(format)
	opts := pdf2img.ConvertOpts{
		Format: format,
		Width:  int(req.Width),
		DPI:    int(req.DPI),
	}

	allPages := req.Pages == PageSelectionAll

	if allPages {
		pageCount, err := p.pdf2img.PageCount(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("page count: %w", err)
		}

		resp := &GenerateThumbnailResponse{}
		for i := 1; i <= pageCount; i++ {
			pageOpts := opts
			pageOpts.Page = i
			result, err := p.pdf2img.ConvertPage(ctx, data, pageOpts)
			if err != nil {
				return nil, fmt.Errorf("convert page %d: %w", i, err)
			}

			tp := ThumbnailPage{
				PageNumber: int32(i),
				SizeBytes:  int64(len(result.Data)),
			}

			if dest := req.Destination; dest != nil {
				ext := path.Ext(dest.Key)
				base := strings.TrimSuffix(dest.Key, ext)
				pageKey := fmt.Sprintf("%s_%03d%s", base, i, ext)
				if err := p.uploadFile(ctx, dest.Bucket, pageKey, mime, result.Data); err != nil {
					return nil, err
				}
				tp.File = &FileRef{Bucket: dest.Bucket, Key: pageKey, ContentType: mime}
			}

			resp.Pages = append(resp.Pages, tp)

			if i == 1 {
				resp.SizeBytes = int64(len(result.Data))
				if tp.File != nil {
					resp.Destination = tp.File
				}
			}
		}
		return resp, nil
	}

	// Single page (default).
	opts.Page = 1
	result, err := p.pdf2img.ConvertPage(ctx, data, opts)
	if err != nil {
		return nil, err
	}

	resp := &GenerateThumbnailResponse{SizeBytes: int64(len(result.Data))}
	if dest := req.Destination; dest != nil {
		if err := p.uploadFile(ctx, dest.Bucket, dest.Key, mime, result.Data); err != nil {
			return nil, err
		}
		resp.Destination = dest
	}
	return resp, nil
}

// ExtractMarkdown extracts markdown from a document.
func (p *Processor) ExtractMarkdown(ctx context.Context, req ExtractMarkdownRequest) (*ExtractMarkdownResponse, error) {
	if err := p.validateFileRef(req.Bucket, req.Key); err != nil {
		return nil, err
	}
	if err := p.validateFileRefPtr(req.DoclingJSONDestination, "docling_json_destination"); err != nil {
		return nil, err
	}
	data, err := p.downloadFile(ctx, req.Bucket, req.Key)
	if err != nil {
		return nil, err
	}
	result, err := p.docling.Convert(ctx, req.Key, data, docling.ConvertOptions{})
	if err != nil {
		return nil, err
	}

	resp := &ExtractMarkdownResponse{
		Markdown: result.Markdown,
		HTML:     result.HTML,
	}
	if dest := req.DoclingJSONDestination; dest != nil {
		if err := p.uploadFile(ctx, dest.Bucket, dest.Key, "application/json", result.DoclingJSON); err != nil {
			return nil, err
		}
		resp.DoclingJSON = dest
	}
	return resp, nil
}

// --- Conversion helpers ---

func jobToInfo(j *jobs.Job) *JobInfo {
	info := &JobInfo{
		JobID:             j.ID,
		ExternalReference: j.ExternalReference,
		Status:            JobStatus(j.Status),
		Error:             j.Error,
	}

	if j.Progress != nil {
		info.Progress = &JobProgress{
			Step:    j.Progress.Step,
			Current: j.Progress.Current,
			Total:   j.Progress.Total,
			Message: j.Progress.Message,
		}
	}

	// Unmarshal results from job Output JSON.
	if len(j.Output) > 0 {
		var output ProcessOutput
		if err := json.Unmarshal(j.Output, &output); err == nil && output.Results != nil {
			info.Results = output.Results
		}
	}

	return info
}
