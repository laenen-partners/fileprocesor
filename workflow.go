package fileprocesor

import (
	"bytes"
	"context"
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
	"github.com/laenen-partners/objectstore"
)

// Processor holds the service dependencies, shared across workflows.
type Processor struct {
	store     objectstore.Store
	scanner   antivirus.Scanner
	gotenberg gotenberg.Converter
	docling   docling.Converter
	pdf2img   pdf2img.Converter
	dbosCtx   dbos.DBOSContext
}

// ProcessInput is the workflow input, serialized from ProcessRequest.
type ProcessInput struct {
	Inputs       []FileInputDef            `json:"inputs"`
	Operations   []OperationDef            `json:"operations"`
	Destinations map[string]FileRefDef     `json:"destinations"`
}

type FileInputDef struct {
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

type FileRefDef struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

type OperationDef struct {
	Name            string           `json:"name"`
	Inputs          []string         `json:"inputs"`
	Scan            *ScanOpDef       `json:"scan,omitempty"`
	ConvertToPDF    *ConvertOpDef    `json:"convert_to_pdf,omitempty"`
	MergePDFs       *MergeOpDef      `json:"merge_pdfs,omitempty"`
	Thumbnail       *ThumbnailOpDef  `json:"thumbnail,omitempty"`
	ExtractMarkdown *MarkdownOpDef   `json:"extract_markdown,omitempty"`
}

type ScanOpDef struct{}
type ConvertOpDef struct{}
type MergeOpDef struct{}
type ThumbnailOpDef struct {
	Width  int32  `json:"width"`
	Dpi    int32  `json:"dpi"`
	Format string `json:"format,omitempty"`
	Pages  string `json:"pages,omitempty"` // "first" or "all"
}
type MarkdownOpDef struct{}

func (o *OperationDef) IsScan() bool { return o.Scan != nil }

// OperationResult is the workflow output for a single operation.
type OperationResultDef struct {
	Success        bool                 `json:"success"`
	Error          string               `json:"error,omitempty"`
	ScanDetail     *ScanDef             `json:"scan,omitempty"`
	MDDetail       *MDDef               `json:"markdown,omitempty"`
	ThumbnailPages []ThumbnailResultDef `json:"thumbnail_pages,omitempty"`
	Destination    *FileRefDef          `json:"destination,omitempty"`
	SizeBytes      int64                `json:"size_bytes,omitempty"`
}

type ScanDef struct {
	Clean     bool   `json:"clean"`
	VirusName string `json:"virus_name,omitempty"`
}

type MDDef struct {
	Markdown string `json:"markdown"`
	HTML     string `json:"html"`
}

type ThumbnailResultDef struct {
	PageNumber  int32      `json:"page_number"`
	Destination FileRefDef `json:"destination"`
	SizeBytes   int64      `json:"size_bytes"`
}

// ProcessOutput is the workflow output.
type ProcessOutput struct {
	Results map[string]*OperationResultDef `json:"results"`
}

// ProcessWorkflow is the DBOS workflow registered at startup.
func (p *Processor) ProcessWorkflow(ctx dbos.DBOSContext, input ProcessInput) (ProcessOutput, error) {
	// Step 1: Download all inputs.
	data := make(map[string][]byte)
	for _, inp := range input.Inputs {
		inp := inp
		fileData, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
			return p.downloadFile(sctx, inp.Bucket, inp.Key)
		}, dbos.WithStepName("download_"+inp.Name))
		if err != nil {
			return ProcessOutput{}, fmt.Errorf("download %s: %w", inp.Name, err)
		}
		data[inp.Name] = fileData
	}

	// Step 2: Execute operations in order.
	results := make(map[string]*OperationResultDef)
	for _, op := range input.Operations {
		result := p.executeOp(ctx, op, data)
		results[op.Name] = result
		// Scan failure is fatal.
		if op.IsScan() && !result.Success {
			break
		}
	}

	// Step 3: Upload outputs to destinations.
	for name, dest := range input.Destinations {
		name, dest := name, dest
		r := results[name]

		// Multi-page thumbnail: upload each page with _NNN suffix.
		if r != nil && len(r.ThumbnailPages) > 1 {
			ext := pathExt(dest.Key)
			base := strings.TrimSuffix(dest.Key, ext)
			for _, pg := range r.ThumbnailPages {
				pg := pg
				dataKey := fmt.Sprintf("%s_p%03d", name, pg.PageNumber)
				pageData := data[dataKey]
				if pageData == nil {
					continue
				}
				pageKey := fmt.Sprintf("%s_%03d%s", base, pg.PageNumber, ext)
				_, err := dbos.RunAsStep(ctx, func(sctx context.Context) (any, error) {
					return nil, p.uploadFile(sctx, dest.Bucket, pageKey, dest.ContentType, pageData)
				}, dbos.WithStepName("upload_"+dataKey))
				if err != nil {
					slog.Error("upload destination failed", "name", dataKey, "error", err)
				}
				pg.Destination = FileRefDef{Bucket: dest.Bucket, Key: pageKey, ContentType: dest.ContentType}
				// Update the result in-place.
				for j := range r.ThumbnailPages {
					if r.ThumbnailPages[j].PageNumber == pg.PageNumber {
						r.ThumbnailPages[j].Destination = pg.Destination
					}
				}
			}
			continue
		}

		if fileData, ok := data[name]; ok {
			_, err := dbos.RunAsStep(ctx, func(sctx context.Context) (any, error) {
				return nil, p.uploadFile(sctx, dest.Bucket, dest.Key, dest.ContentType, fileData)
			}, dbos.WithStepName("upload_"+name))
			if err != nil {
				slog.Error("upload destination failed", "name", name, "error", err)
			}
		}
	}

	return ProcessOutput{Results: results}, nil
}

func (p *Processor) executeOp(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	switch {
	case op.Scan != nil:
		return p.execScan(ctx, op, data)
	case op.ConvertToPDF != nil:
		return p.execConvert(ctx, op, data)
	case op.MergePDFs != nil:
		return p.execMerge(ctx, op, data)
	case op.Thumbnail != nil:
		return p.execThumbnail(ctx, op, data)
	case op.ExtractMarkdown != nil:
		return p.execMarkdown(ctx, op, data)
	default:
		return &OperationResultDef{Error: "unknown operation type"}
	}
}

func (p *Processor) execScan(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	if p.scanner == nil {
		return &OperationResultDef{
			Success:    true,
			ScanDetail: &ScanDef{Clean: true},
		}
	}

	inputData := data[op.Inputs[0]]
	result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (antivirus.ScanResult, error) {
		return p.scanner.Scan(sctx, inputData)
	}, dbos.WithStepName("scan_"+op.Name))
	if err != nil {
		return &OperationResultDef{Error: fmt.Sprintf("scan failed: %v", err)}
	}
	return &OperationResultDef{
		Success:    result.Clean,
		ScanDetail: &ScanDef{Clean: result.Clean, VirusName: result.Detail},
		Error:      boolStr(!result.Clean, "virus detected: "+result.Detail),
	}
}

func (p *Processor) execConvert(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	inputName := op.Inputs[0]
	inputData := data[inputName]
	pdfBytes, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
		return p.gotenberg.ConvertToPDF(sctx, inputName, inputData, "")
	}, dbos.WithStepName("convert_"+op.Name))
	if err != nil {
		return &OperationResultDef{Error: fmt.Sprintf("convert failed: %v", err)}
	}
	data[op.Name] = pdfBytes
	return &OperationResultDef{Success: true, SizeBytes: int64(len(pdfBytes))}
}

func (p *Processor) execMerge(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	pdfs := make([]gotenberg.NamedPDF, 0, len(op.Inputs))
	for _, name := range op.Inputs {
		pdfs = append(pdfs, gotenberg.NamedPDF{Name: name + ".pdf", Data: data[name]})
	}
	merged, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]byte, error) {
		return p.gotenberg.MergePDFs(sctx, pdfs)
	}, dbos.WithStepName("merge_"+op.Name))
	if err != nil {
		return &OperationResultDef{Error: fmt.Sprintf("merge failed: %v", err)}
	}
	data[op.Name] = merged
	return &OperationResultDef{Success: true, SizeBytes: int64(len(merged))}
}

func (p *Processor) execThumbnail(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	inputData := data[op.Inputs[0]]
	opts := pdf2img.ConvertOpts{
		Format: op.Thumbnail.Format,
		Width:  int(op.Thumbnail.Width),
		DPI:    int(op.Thumbnail.Dpi),
	}

	if op.Thumbnail.Pages == "all" {
		pageCount, err := dbos.RunAsStep(ctx, func(sctx context.Context) (int, error) {
			return p.pdf2img.PageCount(sctx, inputData)
		}, dbos.WithStepName("pagecount_"+op.Name))
		if err != nil {
			return &OperationResultDef{Error: fmt.Sprintf("thumbnail page count failed: %v", err)}
		}

		pages := make([]ThumbnailResultDef, 0, pageCount)
		for i := 1; i <= pageCount; i++ {
			pageOpts := opts
			pageOpts.Page = i
			stepName := fmt.Sprintf("thumbnail_%s_p%03d", op.Name, i)
			result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (*pdf2img.PageResult, error) {
				return p.pdf2img.ConvertPage(sctx, inputData, pageOpts)
			}, dbos.WithStepName(stepName))
			if err != nil {
				return &OperationResultDef{Error: fmt.Sprintf("thumbnail page %d failed: %v", i, err)}
			}
			dataKey := fmt.Sprintf("%s_p%03d", op.Name, i)
			data[dataKey] = result.Data
			pages = append(pages, ThumbnailResultDef{
				PageNumber: int32(i),
				SizeBytes:  int64(len(result.Data)),
			})
		}
		// Also store first page under op.Name for backward compat.
		if pageCount > 0 {
			data[op.Name] = data[fmt.Sprintf("%s_p001", op.Name)]
		}
		return &OperationResultDef{
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
		return &OperationResultDef{Error: fmt.Sprintf("thumbnail failed: %v", err)}
	}
	data[op.Name] = result.Data
	return &OperationResultDef{Success: true, SizeBytes: int64(len(result.Data))}
}

func (p *Processor) execMarkdown(ctx dbos.DBOSContext, op OperationDef, data map[string][]byte) *OperationResultDef {
	inputName := op.Inputs[0]
	inputData := data[inputName]
	result, err := dbos.RunAsStep(ctx, func(sctx context.Context) (*docling.ConvertResult, error) {
		return p.docling.Convert(sctx, inputName+".pdf", inputData)
	}, dbos.WithStepName("markdown_"+op.Name))
	if err != nil {
		return &OperationResultDef{Error: fmt.Sprintf("extract markdown failed: %v", err)}
	}
	return &OperationResultDef{
		Success:  true,
		MDDetail: &MDDef{Markdown: result.Markdown, HTML: result.HTML},
	}
}

func (p *Processor) downloadFile(ctx context.Context, bucket, key string) ([]byte, error) {
	rc, err := p.store.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("get object %s/%s: %w", bucket, key, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
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
