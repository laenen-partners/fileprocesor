package fileprocesor

import (
	"context"
	"encoding/json"
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
	"github.com/laenen-partners/jobs"
)

// Handler implements the connect-go FileProcessorService.
type Handler struct {
	fileprocessorv1connect.UnimplementedFileProcessorServiceHandler
	proc *Processor
}

// Process submits a pipeline and returns immediately with a job ID.
func (h *Handler) Process(ctx context.Context, req *connect.Request[fpv1.ProcessRequest]) (*connect.Response[fpv1.ProcessResponse], error) {
	if h.proc.jobs == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job tracking not configured: ENTITY_STORE_URL is required"))
	}

	if err := h.proc.validateProcessRequest(req.Msg); err != nil {
		return nil, err
	}

	input := toProcessInput(req.Msg)

	// Propagate caller identity into workflow input.
	caller := CallerFromContext(ctx)
	if input.OwnerID == "" && caller.UserID != "" {
		input.OwnerID = caller.UserID
	}

	wfID := uuid.NewString()
	handle, err := dbos.RunWorkflow(h.proc.dbosCtx, h.proc.ProcessWorkflow, input,
		dbos.WithWorkflowID(wfID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("start workflow: %w", err))
	}

	// Wait for the job entity ID to be published by the workflow.
	jobID, err := dbos.GetEvent[string](h.proc.dbosCtx, handle.GetWorkflowID(), "job_entity_id", 30)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("waiting for job creation: %w", err))
	}

	return connect.NewResponse(&fpv1.ProcessResponse{
		JobId:      jobID,
		WorkflowId: wfID,
	}), nil
}

// GetJob returns the current state of a processing job.
func (h *Handler) GetJob(ctx context.Context, req *connect.Request[fpv1.GetJobRequest]) (*connect.Response[fpv1.GetJobResponse], error) {
	if h.proc.jobs == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job tracking not configured"))
	}
	if req.Msg.JobId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("job_id is required"))
	}

	job, err := h.proc.jobs.Get(ctx, req.Msg.JobId)
	if err != nil {
		if err == jobs.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("job not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get job: %w", err))
	}

	return connect.NewResponse(jobToProto(job)), nil
}

// ListJobs returns jobs matching the given filters.
func (h *Handler) ListJobs(ctx context.Context, req *connect.Request[fpv1.ListJobsRequest]) (*connect.Response[fpv1.ListJobsResponse], error) {
	if h.proc.jobs == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job tracking not configured"))
	}

	filter := jobs.ListFilter{
		OwnerID: req.Msg.OwnerId,
		Status:  req.Msg.Status,
		JobType: "file_processing",
		Limit:   int(req.Msg.Limit),
		Offset:  int(req.Msg.Offset),
	}

	jobList, err := h.proc.jobs.List(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list jobs: %w", err))
	}

	resp := &fpv1.ListJobsResponse{
		Jobs: make([]*fpv1.GetJobResponse, 0, len(jobList)),
	}
	for _, j := range jobList {
		resp.Jobs = append(resp.Jobs, jobToProto(&j))
	}
	return connect.NewResponse(resp), nil
}

// CancelJob marks a running job as cancelled.
func (h *Handler) CancelJob(ctx context.Context, req *connect.Request[fpv1.CancelJobRequest]) (*connect.Response[fpv1.CancelJobResponse], error) {
	if h.proc.jobs == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job tracking not configured"))
	}
	if req.Msg.JobId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("job_id is required"))
	}

	if err := h.proc.jobs.Cancel(ctx, req.Msg.JobId); err != nil {
		if err == jobs.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("job not found"))
		}
		if err == jobs.ErrAlreadyFinalized {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job already finalized"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cancel job: %w", err))
	}

	return connect.NewResponse(&fpv1.CancelJobResponse{}), nil
}

func (h *Handler) ScanFile(ctx context.Context, req *connect.Request[fpv1.ScanFileRequest]) (*connect.Response[fpv1.ScanFileResponse], error) {
	if err := h.proc.validateFileRef(req.Msg.Bucket, req.Msg.Key); err != nil {
		return nil, err
	}
	if h.proc.scanner == nil {
		return connect.NewResponse(&fpv1.ScanFileResponse{Clean: false, Detail: "scanning disabled: no antivirus configured"}), nil
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
	if err := h.proc.validateFileRef(req.Msg.Bucket, req.Msg.Key); err != nil {
		return nil, err
	}
	if req.Msg.ContentType == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content_type is required for conversion routing"))
	}
	if err := h.proc.validateFileRefMsg(req.Msg.Destination, "destination"); err != nil {
		return nil, err
	}
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
	if len(req.Msg.Files) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one file is required"))
	}
	for _, f := range req.Msg.Files {
		if err := h.proc.validateFileRef(f.Bucket, f.Key); err != nil {
			return nil, err
		}
	}
	if err := h.proc.validateFileRefMsg(req.Msg.Destination, "destination"); err != nil {
		return nil, err
	}
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
	if err := h.proc.validateFileRef(req.Msg.Bucket, req.Msg.Key); err != nil {
		return nil, err
	}
	if err := h.proc.validateFileRefMsg(req.Msg.Destination, "destination"); err != nil {
		return nil, err
	}
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
					resp.Result = tp.File //nolint:staticcheck // deprecated but still populated for backwards compat
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
		resp.Result = dest //nolint:staticcheck // deprecated but still populated for backwards compat
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) ExtractMarkdown(ctx context.Context, req *connect.Request[fpv1.ExtractMarkdownRequest]) (*connect.Response[fpv1.ExtractMarkdownResponse], error) {
	if err := h.proc.validateFileRef(req.Msg.Bucket, req.Msg.Key); err != nil {
		return nil, err
	}
	if err := h.proc.validateFileRefMsg(req.Msg.DoclingJsonDestination, "docling_json_destination"); err != nil {
		return nil, err
	}
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

func jobToProto(j *jobs.Job) *fpv1.GetJobResponse {
	resp := &fpv1.GetJobResponse{
		JobId:      j.ID,
		WorkflowId: j.WorkflowID,
		Status:     statusToProto(j.Status),
		Error:      j.Error,
	}

	if j.Progress != nil {
		resp.Progress = &fpv1.JobProgress{
			Step:    j.Progress.Step,
			Current: int32(j.Progress.Current),
			Total:   int32(j.Progress.Total),
			Message: j.Progress.Message,
		}
	}

	// Unmarshal results from job Output JSON.
	if len(j.Output) > 0 {
		var output ProcessOutput
		if err := json.Unmarshal(j.Output, &output); err == nil && output.Results != nil {
			resp.Results = make(map[string]*fpv1.OperationResult)
			for name, r := range output.Results {
				resp.Results[name] = operationResultToProto(r)
			}
		}
	}

	return resp
}

func statusToProto(s string) fpv1.JobStatus {
	switch s {
	case jobs.StatusPending:
		return fpv1.JobStatus_JOB_STATUS_PENDING
	case jobs.StatusRunning:
		return fpv1.JobStatus_JOB_STATUS_RUNNING
	case jobs.StatusCompleted:
		return fpv1.JobStatus_JOB_STATUS_COMPLETED
	case jobs.StatusFailed:
		return fpv1.JobStatus_JOB_STATUS_FAILED
	case jobs.StatusCancelled:
		return fpv1.JobStatus_JOB_STATUS_CANCELLED
	default:
		return fpv1.JobStatus_JOB_STATUS_UNSPECIFIED
	}
}

func operationResultToProto(r *OperationResultDef) *fpv1.OperationResult {
	or := &fpv1.OperationResult{
		Success:   r.Success,
		Error:     r.Error,
		SizeBytes: r.SizeBytes,
	}
	if r.ScanDetail != nil {
		or.Detail = &fpv1.OperationResult_Scan{
			Scan: &fpv1.ScanDetail{
				Clean:  r.ScanDetail.Clean,
				Detail: r.ScanDetail.Detail,
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
	return or
}

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
