package fileprocesor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/laenen-partners/fileprocesor/antivirus"
	"github.com/laenen-partners/fileprocesor/docling"
	"github.com/laenen-partners/fileprocesor/gotenberg"
	"github.com/laenen-partners/fileprocesor/pdf2img"
	"github.com/laenen-partners/jobs"
	"github.com/laenen-partners/objectstore"
)

// Processor holds the service dependencies.
type Processor struct {
	store          objectstore.Store
	scanner        antivirus.Scanner
	gotenberg      gotenberg.Converter
	docling        docling.Converter
	pdf2img        pdf2img.Converter
	jobs           *jobs.Client
	dbosCtx        dbos.DBOSContext
	maxFileSize    int64
	allowedBuckets map[string]bool // nil = allow all
}

// ProcessInput describes a processing pipeline.
type ProcessInput struct {
	Inputs       []FileInput        `json:"inputs"`
	Operations   []Operation        `json:"operations"`
	Destinations map[string]FileRef `json:"destinations"`
	Tags         []string           `json:"tags,omitempty"`
}

// FileInput describes an input file to be downloaded.
type FileInput struct {
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

// FileRef describes a reference to a file in object storage.
type FileRef struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

// Operation describes a single processing step in the pipeline.
type Operation struct {
	Name            string             `json:"name"`
	Inputs          []string           `json:"inputs"`
	Scan            *ScanOp            `json:"scan,omitempty"`
	ConvertToPDF    *ConvertToPDFOp    `json:"convert_to_pdf,omitempty"`
	MergePDFs       *MergePDFsOp       `json:"merge_pdfs,omitempty"`
	Thumbnail       *ThumbnailOp       `json:"thumbnail,omitempty"`
	ExtractMarkdown *ExtractMarkdownOp `json:"extract_markdown,omitempty"`
}

// ScanOp configures an antivirus scan operation.
type ScanOp struct{}

// ConvertToPDFOp configures a PDF conversion operation.
type ConvertToPDFOp struct{}

// MergePDFsOp configures a PDF merge operation.
type MergePDFsOp struct{}

// ThumbnailOp configures a thumbnail generation operation.
type ThumbnailOp struct {
	Width  int32  `json:"width"`
	DPI    int32  `json:"dpi"`
	Format string `json:"format,omitempty"`
	Pages  string `json:"pages,omitempty"` // "first" or "all"
}

// ExtractMarkdownOp configures a markdown extraction operation.
type ExtractMarkdownOp struct{}

func (o *Operation) isScan() bool { return o.Scan != nil }

// OperationResult is the output for a single operation.
type OperationResult struct {
	Success        bool              `json:"success"`
	Error          string            `json:"error,omitempty"`
	ScanDetail     *ScanDetail       `json:"scan,omitempty"`
	MDDetail       *MarkdownDetail   `json:"markdown,omitempty"`
	ThumbnailPages []ThumbnailDetail `json:"thumbnail_pages,omitempty"`
	Destination    *FileRef          `json:"destination,omitempty"`
	SizeBytes      int64             `json:"size_bytes,omitempty"`
}

// ScanDetail contains antivirus scan results.
type ScanDetail struct {
	Clean  bool   `json:"clean"`
	Detail string `json:"detail,omitempty"`
}

// MarkdownDetail contains markdown extraction results.
type MarkdownDetail struct {
	Markdown string          `json:"markdown"`
	HTML     string          `json:"html"`
	Chunks   []docling.Chunk `json:"chunks,omitempty"`
}

// ThumbnailDetail describes one page of a multi-page thumbnail result.
type ThumbnailDetail struct {
	PageNumber  int32   `json:"page_number"`
	Destination FileRef `json:"destination"`
	SizeBytes   int64   `json:"size_bytes"`
}

// ProcessOutput is the pipeline output.
type ProcessOutput struct {
	Results map[string]*OperationResult `json:"results"`
}

// ProcessWorkflow is the DBOS workflow registered at startup.
func (p *Processor) ProcessWorkflow(ctx dbos.DBOSContext, input ProcessInput) (output ProcessOutput, err error) {
	// Step 0: Publish job (if jobs client is available).
	var jobID string
	if p.jobs != nil {
		wfID, _ := dbos.GetWorkflowID(ctx)
		job, pubErr := p.jobs.RegisterJob(ctx, jobs.RegisterJobParams{
			ExternalReference: wfID,
			JobType:           "file_processing",
			Tags:              append([]string{"file_processing"}, input.Tags...),
		})
		if pubErr != nil {
			return ProcessOutput{}, fmt.Errorf("publish job: %w", pubErr)
		}
		jobID = job.ID
		dbos.SetEvent(ctx, "job_entity_id", jobID)

		// Defer finalize — runs on success, failure, or panic.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}

			status := jobs.StatusCompleted
			var errDetail string
			if err != nil {
				status = jobs.StatusFailed
				errDetail = err.Error()
			}

			var resultsJSON json.RawMessage
			if output.Results != nil {
				if b, jsonErr := json.Marshal(output); jsonErr == nil {
					resultsJSON = b
				}
			}

			finalizeErr := p.jobs.FinalizeJob(ctx, jobID, jobs.FinalizeParams{
				Status: status,
				Error:  errDetail,
				Output: resultsJSON,
			})
			if finalizeErr != nil {
				slog.Error("finalize job failed", "job_id", jobID, "error", finalizeErr)
			}
		}()
	}

	// Step 1: Download all inputs.
	if p.jobs != nil && jobID != "" {
		_ = p.jobs.ReportProgress(ctx, jobID, jobs.Progress{Step: "downloading", Current: 0, Total: len(input.Inputs)})
	}

	data := make(map[string][]byte)
	keys := make(map[string]string) // name → original key/filename
	for i, inp := range input.Inputs {
		slog.Info("workflow: downloading input", "name", inp.Name, "bucket", inp.Bucket, "key", inp.Key)
		fileData, dlErr := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
			return p.downloadFile(sctx, inp.Bucket, inp.Key)
		}, dbos.WithStepName("download_"+inp.Name))
		if dlErr != nil {
			return ProcessOutput{}, fmt.Errorf("download %s: %w", inp.Name, dlErr)
		}
		slog.Info("workflow: downloaded input", "name", inp.Name, "size", len(fileData))
		data[inp.Name] = fileData
		keys[inp.Name] = inp.Key

		if p.jobs != nil && jobID != "" {
			_ = p.jobs.ReportProgress(ctx, jobID, jobs.Progress{Step: "downloading", Current: i + 1, Total: len(input.Inputs)})
		}
	}

	// Step 2: Execute operations in order.
	refCount := buildRefCounts(input)

	results := make(map[string]*OperationResult)
	scanFailed := false
	for i, op := range input.Operations {
		if scanFailed {
			results[op.Name] = &OperationResult{Error: "skipped: prior scan detected a threat"}
			continue
		}

		if p.jobs != nil && jobID != "" {
			_ = p.jobs.ReportProgress(ctx, jobID, jobs.Progress{Step: op.Name, Current: i + 1, Total: len(input.Operations)})
		}

		slog.Info("workflow: executing operation", "op", op.Name, "inputs", op.Inputs)
		result := p.executeOp(ctx, op, data, keys)
		results[op.Name] = result
		slog.Info("workflow: operation complete", "op", op.Name, "success", result.Success, "error", result.Error, "size", result.SizeBytes)

		// Free inputs that are no longer referenced.
		for _, ref := range op.Inputs {
			refCount[ref]--
			if refCount[ref] <= 0 {
				delete(data, ref)
			}
		}

		if op.isScan() && !result.Success {
			scanFailed = true
		}
	}

	// Step 3: Upload outputs to destinations.
	for name, dest := range input.Destinations {
		r := results[name]

		// Multi-page thumbnail: upload each page with _NNN suffix.
		if r != nil && len(r.ThumbnailPages) > 1 {
			ext := pathExt(dest.Key)
			base := strings.TrimSuffix(dest.Key, ext)
			for _, pg := range r.ThumbnailPages {
				dataKey := fmt.Sprintf("%s_p%03d", name, pg.PageNumber)
				pageData := data[dataKey]
				if pageData == nil {
					continue
				}
				pageKey := fmt.Sprintf("%s_%03d%s", base, pg.PageNumber, ext)
				_, uploadErr := dbos.RunAsStep(ctx, func(sctx context.Context) (any, error) {
					return nil, p.uploadFile(sctx, dest.Bucket, pageKey, dest.ContentType, pageData)
				}, dbos.WithStepName("upload_"+dataKey))
				if uploadErr != nil {
					slog.Error("upload destination failed", "name", dataKey, "error", uploadErr)
				}
				pg.Destination = FileRef{Bucket: dest.Bucket, Key: pageKey, ContentType: dest.ContentType}
				for j := range r.ThumbnailPages {
					if r.ThumbnailPages[j].PageNumber == pg.PageNumber {
						r.ThumbnailPages[j].Destination = pg.Destination
					}
				}
			}
			continue
		}

		if fileData, ok := data[name]; ok {
			slog.Info("workflow: uploading output", "name", name, "bucket", dest.Bucket, "key", dest.Key, "size", len(fileData))
			_, uploadErr := dbos.RunAsStep(ctx, func(sctx context.Context) (any, error) {
				return nil, p.uploadFile(sctx, dest.Bucket, dest.Key, dest.ContentType, fileData)
			}, dbos.WithStepName("upload_"+name))
			if uploadErr != nil {
				slog.Error("upload destination failed", "name", name, "error", uploadErr)
			}
		}
	}

	output = ProcessOutput{Results: results}
	return output, nil
}

func (p *Processor) executeOp(ctx dbos.DBOSContext, op Operation, data map[string][]byte, keys map[string]string) *OperationResult {
	switch {
	case op.Scan != nil:
		return p.execScan(ctx, op, data)
	case op.ConvertToPDF != nil:
		return p.execConvert(ctx, op, data, keys)
	case op.MergePDFs != nil:
		return p.execMerge(ctx, op, data)
	case op.Thumbnail != nil:
		return p.execThumbnail(ctx, op, data)
	case op.ExtractMarkdown != nil:
		return p.execMarkdown(ctx, op, data)
	default:
		return &OperationResult{Error: "unknown operation type"}
	}
}

func (p *Processor) execScan(ctx dbos.DBOSContext, op Operation, data map[string][]byte) *OperationResult {
	if p.scanner == nil {
		return &OperationResult{
			Success:    false,
			ScanDetail: &ScanDetail{Clean: false},
			Error:      "scanning disabled: no antivirus configured",
		}
	}

	inputData := data[op.Inputs[0]]
	result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (antivirus.ScanResult, error) {
		return p.scanner.Scan(sctx, inputData)
	}, dbos.WithStepName("scan_"+op.Name))
	if err != nil {
		return &OperationResult{Error: fmt.Sprintf("scan failed: %v", err)}
	}
	return &OperationResult{
		Success:    result.Clean,
		ScanDetail: &ScanDetail{Clean: result.Clean, Detail: result.Detail},
		Error:      boolStr(!result.Clean, "virus detected: "+result.Detail),
	}
}

func (p *Processor) execConvert(ctx dbos.DBOSContext, op Operation, data map[string][]byte, keys map[string]string) *OperationResult {
	inputName := op.Inputs[0]
	inputData := data[inputName]
	fileName := keys[inputName]
	if fileName == "" {
		fileName = inputName
	}
	pdfBytes, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
		return p.gotenberg.ConvertToPDF(sctx, fileName, inputData, "")
	}, dbos.WithStepName("convert_"+op.Name))
	if err != nil {
		return &OperationResult{Error: fmt.Sprintf("convert failed: %v", err)}
	}
	data[op.Name] = pdfBytes
	keys[op.Name] = op.Name + ".pdf"
	return &OperationResult{Success: true, SizeBytes: int64(len(pdfBytes))}
}

func (p *Processor) execMerge(ctx dbos.DBOSContext, op Operation, data map[string][]byte) *OperationResult {
	pdfs := make([]gotenberg.NamedPDF, 0, len(op.Inputs))
	for _, name := range op.Inputs {
		pdfs = append(pdfs, gotenberg.NamedPDF{Name: name + ".pdf", Data: data[name]})
	}
	merged, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
		return p.gotenberg.MergePDFs(sctx, pdfs)
	}, dbos.WithStepName("merge_"+op.Name))
	if err != nil {
		return &OperationResult{Error: fmt.Sprintf("merge failed: %v", err)}
	}
	data[op.Name] = merged
	return &OperationResult{Success: true, SizeBytes: int64(len(merged))}
}

func (p *Processor) execThumbnail(ctx dbos.DBOSContext, op Operation, data map[string][]byte) *OperationResult {
	inputData := data[op.Inputs[0]]
	opts := pdf2img.ConvertOpts{
		Format: op.Thumbnail.Format,
		Width:  int(op.Thumbnail.Width),
		DPI:    int(op.Thumbnail.DPI),
	}

	if op.Thumbnail.Pages == "all" {
		pageCount, err := dbos.RunAsStep(ctx, func(sctx context.Context) (int, error) {
			return p.pdf2img.PageCount(sctx, inputData)
		}, dbos.WithStepName("pagecount_"+op.Name))
		if err != nil {
			return &OperationResult{Error: fmt.Sprintf("thumbnail page count failed: %v", err)}
		}

		pages := make([]ThumbnailDetail, 0, pageCount)
		for i := 1; i <= pageCount; i++ {
			pageOpts := opts
			pageOpts.Page = i
			stepName := fmt.Sprintf("thumbnail_%s_p%03d", op.Name, i)
			result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (*pdf2img.PageResult, error) {
				return p.pdf2img.ConvertPage(sctx, inputData, pageOpts)
			}, dbos.WithStepName(stepName))
			if err != nil {
				return &OperationResult{Error: fmt.Sprintf("thumbnail page %d failed: %v", i, err)}
			}
			dataKey := fmt.Sprintf("%s_p%03d", op.Name, i)
			data[dataKey] = result.Data
			pages = append(pages, ThumbnailDetail{
				PageNumber: int32(i),
				SizeBytes:  int64(len(result.Data)),
			})
		}
		if pageCount > 0 {
			data[op.Name] = data[fmt.Sprintf("%s_p001", op.Name)]
		}
		return &OperationResult{
			Success:        true,
			SizeBytes:      pages[0].SizeBytes,
			ThumbnailPages: pages,
		}
	}

	// Single page (default).
	opts.Page = 1
	result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (*pdf2img.PageResult, error) {
		return p.pdf2img.ConvertPage(sctx, inputData, opts)
	}, dbos.WithStepName("thumbnail_"+op.Name))
	if err != nil {
		return &OperationResult{Error: fmt.Sprintf("thumbnail failed: %v", err)}
	}
	data[op.Name] = result.Data
	return &OperationResult{Success: true, SizeBytes: int64(len(result.Data))}
}

func (p *Processor) execMarkdown(ctx dbos.DBOSContext, op Operation, data map[string][]byte) *OperationResult {
	inputName := op.Inputs[0]
	inputData := data[inputName]
	result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (*docling.ConvertResult, error) {
		return p.docling.Convert(sctx, inputName+".pdf", inputData, docling.ConvertOptions{})
	}, dbos.WithStepName("markdown_"+op.Name))
	if err != nil {
		return &OperationResult{Error: fmt.Sprintf("extract markdown failed: %v", err)}
	}
	return &OperationResult{
		Success:  true,
		MDDetail: &MarkdownDetail{Markdown: result.Markdown, HTML: result.HTML, Chunks: result.Chunks},
	}
}

func (p *Processor) downloadFile(ctx context.Context, bucket, key string) ([]byte, error) {
	rc, err := p.store.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("get object %s/%s: %w", bucket, key, err)
	}
	defer rc.Close()

	r := io.LimitReader(rc, p.maxFileSize+1)
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read object %s/%s: %w", bucket, key, err)
	}
	if int64(len(data)) > p.maxFileSize {
		return nil, fmt.Errorf("object %s/%s exceeds max file size (%d bytes)", bucket, key, p.maxFileSize)
	}
	return data, nil
}

func (p *Processor) uploadFile(ctx context.Context, bucket, key, contentType string, data []byte) error {
	return p.store.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(len(data)), contentType)
}

func boolStr(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

func pathExt(p string) string {
	return path.Ext(p)
}

func buildRefCounts(input ProcessInput) map[string]int {
	rc := make(map[string]int)
	for _, op := range input.Operations {
		for _, ref := range op.Inputs {
			rc[ref]++
		}
	}
	for name := range input.Destinations {
		rc[name]++
	}
	return rc
}
