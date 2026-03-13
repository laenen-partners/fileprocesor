package fileprocesor

// ImageFormat specifies the output image format for thumbnail generation.
type ImageFormat string

const (
	ImageFormatJPEG ImageFormat = "jpg"
	ImageFormatPNG  ImageFormat = "png"
	ImageFormatWEBP ImageFormat = "webp"
)

// PageSelection specifies which pages to convert for thumbnail generation.
type PageSelection string

const (
	PageSelectionFirst PageSelection = "first"
	PageSelectionAll   PageSelection = "all"
)

// JobStatus represents the current state of a processing job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// --- Standalone request/response types ---

// ScanFileRequest is the input for the ScanFile method.
type ScanFileRequest struct {
	Bucket string
	Key    string
}

// ScanFileResponse is the output of the ScanFile method.
type ScanFileResponse struct {
	Clean  bool
	Detail string
}

// ConvertToPDFRequest is the input for the ConvertToPDF method.
type ConvertToPDFRequest struct {
	Bucket      string
	Key         string
	ContentType string
	Destination *FileRef
}

// ConvertToPDFResponse is the output of the ConvertToPDF method.
type ConvertToPDFResponse struct {
	SizeBytes   int64
	Destination *FileRef
}

// MergePDFsRequest is the input for the MergePDFs method.
type MergePDFsRequest struct {
	Files       []FileRef
	Destination *FileRef
}

// MergePDFsResponse is the output of the MergePDFs method.
type MergePDFsResponse struct {
	SizeBytes   int64
	Destination *FileRef
}

// GenerateThumbnailRequest is the input for the GenerateThumbnail method.
type GenerateThumbnailRequest struct {
	Bucket      string
	Key         string
	Format      ImageFormat
	Pages       PageSelection
	Width       int32
	DPI         int32
	Destination *FileRef
}

// GenerateThumbnailResponse is the output of the GenerateThumbnail method.
type GenerateThumbnailResponse struct {
	SizeBytes   int64
	Destination *FileRef
	Pages       []ThumbnailPage
}

// ThumbnailPage describes one page of a multi-page thumbnail result.
type ThumbnailPage struct {
	PageNumber int32
	SizeBytes  int64
	File       *FileRef
}

// ExtractMarkdownRequest is the input for the ExtractMarkdown method.
type ExtractMarkdownRequest struct {
	Bucket                string
	Key                   string
	DoclingJSONDestination *FileRef
}

// ExtractMarkdownResponse is the output of the ExtractMarkdown method.
type ExtractMarkdownResponse struct {
	Markdown    string
	HTML        string
	DoclingJSON *FileRef
}

// --- Job types ---

// ProcessResponse is returned by Process with the job and workflow IDs.
type ProcessResponse struct {
	JobID             string
	ExternalReference string // DBOS workflow ID
}

// JobInfo describes the current state of a processing job.
type JobInfo struct {
	JobID             string
	ExternalReference string // DBOS workflow ID
	Status            JobStatus
	Progress          *JobProgress
	Error             string
	Results           map[string]*OperationResult
}

// JobProgress describes the progress of a running job.
type JobProgress struct {
	Step    string
	Current int
	Total   int
	Message string
}

// ListJobsFilter specifies filters for listing jobs.
// Tags are AND-combined with the implicit "file_processing" tag.
type ListJobsFilter struct {
	Tags   []string
	Limit  int
	Offset int
}
