package fileprocesor

import (
	"context"
	"fmt"
	"path"
	"strings"

	"connectrpc.com/connect"
	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	fpv1 "github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1"
	"github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1/fileprocessorv1connect"
	"github.com/laenen-partners/fileprocesor/gotenberg"
	"github.com/laenen-partners/fileprocesor/pdf2img"
)

// Handler implements the connect-go FileProcessorService.
type Handler struct {
	fileprocessorv1connect.UnimplementedFileProcessorServiceHandler
	proc *Processor
}

func (h *Handler) Process(ctx context.Context, req *connect.Request[fpv1.ProcessRequest]) (*connect.Response[fpv1.ProcessResponse], error) {
	input := toProcessInput(req.Msg)
	wfID := uuid.NewString()
	handle, err := dbos.RunWorkflow(h.proc.dbosCtx, h.proc.ProcessWorkflow, input,
		dbos.WithWorkflowID(wfID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("start workflow: %w", err))
	}
	result, err := handle.GetResult()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("workflow failed: %w", err))
	}
	return connect.NewResponse(toProtoResponse(wfID, result)), nil
}

func (h *Handler) ScanFile(ctx context.Context, req *connect.Request[fpv1.ScanFileRequest]) (*connect.Response[fpv1.ScanFileResponse], error) {
	if h.proc.scanner == nil {
		return connect.NewResponse(&fpv1.ScanFileResponse{Clean: true, Detail: "scanning disabled"}), nil
	}

	data, err := h.proc.downloadFile(ctx, req.Msg.Bucket, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	result, err := h.proc.scanner.Scan(ctx, data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&fpv1.ScanFileResponse{
		Clean:  result.Clean,
		Detail: result.Detail,
	}), nil
}

func (h *Handler) ConvertToPDF(ctx context.Context, req *connect.Request[fpv1.ConvertToPDFRequest]) (*connect.Response[fpv1.ConvertToPDFResponse], error) {
	data, err := h.proc.downloadFile(ctx, req.Msg.Bucket, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	pdfBytes, err := h.proc.gotenberg.ConvertToPDF(ctx, req.Msg.Key, data, req.Msg.ContentType)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &fpv1.ConvertToPDFResponse{SizeBytes: int64(len(pdfBytes))}
	if dest := req.Msg.Destination; dest != nil {
		if err := h.proc.uploadFile(ctx, dest.Bucket, dest.Key, "application/pdf", pdfBytes); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Result = dest
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) MergePDFs(ctx context.Context, req *connect.Request[fpv1.MergePDFsRequest]) (*connect.Response[fpv1.MergePDFsResponse], error) {
	pdfs := make([]gotenberg.NamedPDF, 0, len(req.Msg.Files))
	for _, f := range req.Msg.Files {
		data, err := h.proc.downloadFile(ctx, f.Bucket, f.Key)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		pdfs = append(pdfs, gotenberg.NamedPDF{Name: f.Key, Data: data})
	}
	merged, err := h.proc.gotenberg.MergePDFs(ctx, pdfs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &fpv1.MergePDFsResponse{SizeBytes: int64(len(merged))}
	if dest := req.Msg.Destination; dest != nil {
		if err := h.proc.uploadFile(ctx, dest.Bucket, dest.Key, "application/pdf", merged); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Result = dest
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) GenerateThumbnail(ctx context.Context, req *connect.Request[fpv1.GenerateThumbnailRequest]) (*connect.Response[fpv1.GenerateThumbnailResponse], error) {
	data, err := h.proc.downloadFile(ctx, req.Msg.Bucket, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	format := imageFormatToString(req.Msg.Format)
	mime := pdf2img.FormatToMIME(format)
	opts := pdf2img.ConvertOpts{
		Format: format,
		Width:  int(req.Msg.Width),
		DPI:    int(req.Msg.Dpi),
	}

	allPages := pageSelectionIsAll(req.Msg.Pages)

	if allPages {
		pageCount, err := h.proc.pdf2img.PageCount(ctx, data)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("page count: %w", err))
		}

		resp := &fpv1.GenerateThumbnailResponse{}
		for i := 1; i <= pageCount; i++ {
			pageOpts := opts
			pageOpts.Page = i
			result, err := h.proc.pdf2img.ConvertPage(ctx, data, pageOpts)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("convert page %d: %w", i, err))
			}

			tp := &fpv1.ThumbnailPage{
				PageNumber: int32(i),
				SizeBytes:  int64(len(result.Data)),
			}

			if dest := req.Msg.Destination; dest != nil {
				ext := path.Ext(dest.Key)
				base := strings.TrimSuffix(dest.Key, ext)
				pageKey := fmt.Sprintf("%s_%03d%s", base, i, ext)
				if err := h.proc.uploadFile(ctx, dest.Bucket, pageKey, mime, result.Data); err != nil {
					return nil, connect.NewError(connect.CodeInternal, err)
				}
				tp.File = &fpv1.FileRef{Bucket: dest.Bucket, Key: pageKey, ContentType: mime}
			}

			resp.Results = append(resp.Results, tp)

			// First page populates legacy fields.
			if i == 1 {
				resp.SizeBytes = int64(len(result.Data))
				if tp.File != nil {
					resp.Result = tp.File
				}
			}
		}
		return connect.NewResponse(resp), nil
	}

	// Single page (default).
	opts.Page = 1
	result, err := h.proc.pdf2img.ConvertPage(ctx, data, opts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &fpv1.GenerateThumbnailResponse{SizeBytes: int64(len(result.Data))}
	if dest := req.Msg.Destination; dest != nil {
		if err := h.proc.uploadFile(ctx, dest.Bucket, dest.Key, mime, result.Data); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Result = dest
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) ExtractMarkdown(ctx context.Context, req *connect.Request[fpv1.ExtractMarkdownRequest]) (*connect.Response[fpv1.ExtractMarkdownResponse], error) {
	data, err := h.proc.downloadFile(ctx, req.Msg.Bucket, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	result, err := h.proc.docling.Convert(ctx, req.Msg.Key, data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &fpv1.ExtractMarkdownResponse{
		Markdown: result.Markdown,
		Html:     result.HTML,
	}
	if dest := req.Msg.DoclingJsonDestination; dest != nil {
		if err := h.proc.uploadFile(ctx, dest.Bucket, dest.Key, "application/json", result.DoclingJSON); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.DoclingJson = dest
	}
	return connect.NewResponse(resp), nil
}

// --- Conversion helpers ---

func toProcessInput(msg *fpv1.ProcessRequest) ProcessInput {
	pi := ProcessInput{
		Inputs:       make([]FileInputDef, 0, len(msg.Inputs)),
		Operations:   make([]OperationDef, 0, len(msg.Operations)),
		Destinations: make(map[string]FileRefDef),
	}
	for _, inp := range msg.Inputs {
		pi.Inputs = append(pi.Inputs, FileInputDef{
			Name:        inp.Name,
			Bucket:      inp.Bucket,
			Key:         inp.Key,
			ContentType: inp.ContentType,
		})
	}
	for _, op := range msg.Operations {
		od := OperationDef{
			Name:   op.Name,
			Inputs: op.Inputs,
		}
		switch v := op.Op.(type) {
		case *fpv1.Operation_Scan:
			_ = v
			od.Scan = &ScanOpDef{}
		case *fpv1.Operation_ConvertToPdf:
			_ = v
			od.ConvertToPDF = &ConvertOpDef{}
		case *fpv1.Operation_MergePdfs:
			_ = v
			od.MergePDFs = &MergeOpDef{}
		case *fpv1.Operation_Thumbnail:
			od.Thumbnail = &ThumbnailOpDef{
				Width:  v.Thumbnail.Width,
				Dpi:    v.Thumbnail.Dpi,
				Format: imageFormatToString(v.Thumbnail.Format),
				Pages:  pageSelectionToString(v.Thumbnail.Pages),
			}
		case *fpv1.Operation_ExtractMarkdown:
			_ = v
			od.ExtractMarkdown = &MarkdownOpDef{}
		}
		pi.Operations = append(pi.Operations, od)
	}
	for name, ref := range msg.Destinations {
		pi.Destinations[name] = FileRefDef{
			Bucket:      ref.Bucket,
			Key:         ref.Key,
			ContentType: ref.ContentType,
		}
	}
	return pi
}

func toProtoResponse(workflowID string, output ProcessOutput) *fpv1.ProcessResponse {
	resp := &fpv1.ProcessResponse{
		WorkflowId: workflowID,
		Results:    make(map[string]*fpv1.OperationResult),
	}

	for name, r := range output.Results {
		or := &fpv1.OperationResult{
			Success:   r.Success,
			Error:     r.Error,
			SizeBytes: r.SizeBytes,
		}
		if r.ScanDetail != nil {
			or.Detail = &fpv1.OperationResult_Scan{
				Scan: &fpv1.ScanDetail{
					Clean:     r.ScanDetail.Clean,
					VirusName: r.ScanDetail.VirusName,
				},
			}
		}
		if r.MDDetail != nil {
			or.Detail = &fpv1.OperationResult_Markdown{
				Markdown: &fpv1.MarkdownDetail{
					Markdown: r.MDDetail.Markdown,
					Html:     r.MDDetail.HTML,
				},
			}
		}
		if len(r.ThumbnailPages) > 0 {
			td := &fpv1.ThumbnailDetail{}
			for _, pg := range r.ThumbnailPages {
				tp := &fpv1.ThumbnailPage{
					PageNumber: pg.PageNumber,
					SizeBytes:  pg.SizeBytes,
				}
				if pg.Destination.Key != "" {
					tp.File = &fpv1.FileRef{
						Bucket:      pg.Destination.Bucket,
						Key:         pg.Destination.Key,
						ContentType: pg.Destination.ContentType,
					}
				}
				td.Pages = append(td.Pages, tp)
			}
			or.Detail = &fpv1.OperationResult_Thumbnail{Thumbnail: td}
		}
		if r.Destination != nil {
			or.Destination = &fpv1.FileRef{
				Bucket:      r.Destination.Bucket,
				Key:         r.Destination.Key,
				ContentType: r.Destination.ContentType,
			}
		}
		resp.Results[name] = or
	}
	return resp
}

func imageFormatToString(f fpv1.ImageFormat) string {
	switch f {
	case fpv1.ImageFormat_IMAGE_FORMAT_PNG:
		return "png"
	case fpv1.ImageFormat_IMAGE_FORMAT_WEBP:
		return "webp"
	default:
		return "jpg"
	}
}

func pageSelectionToString(ps fpv1.PageSelection) string {
	switch ps {
	case fpv1.PageSelection_PAGE_SELECTION_ALL:
		return "all"
	default:
		return "first"
	}
}

func pageSelectionIsAll(ps fpv1.PageSelection) bool {
	return ps == fpv1.PageSelection_PAGE_SELECTION_ALL
}
