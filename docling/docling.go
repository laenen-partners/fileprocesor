package docling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// maxResponseSize is the maximum size of a Docling response body (512 MB).
const maxResponseSize = 512 << 20

// ConvertResult holds the output from Docling conversion.
type ConvertResult struct {
	Markdown    string
	HTML        string
	DoclingJSON json.RawMessage
	Chunks      []Chunk
}

// ImageExportMode controls how images appear in Docling's text output formats.
type ImageExportMode string

const (
	// ImageExportModePlaceholder replaces images with a placeholder comment.
	// Use this for NLP pipelines (embedding, extraction) where base64 blobs are noise.
	ImageExportModePlaceholder ImageExportMode = "placeholder"
	// ImageExportModeEmbedded inlines images as base64 data URIs (Docling default).
	ImageExportModeEmbedded ImageExportMode = "embedded"
	// ImageExportModeReferenced uses file references instead of inline data.
	ImageExportModeReferenced ImageExportMode = "referenced"
)

// ChunkerType selects the Docling chunking algorithm.
type ChunkerType string

const (
	// ChunkerHybrid uses the hybrid chunker (default).
	ChunkerHybrid ChunkerType = "hybrid"
	// ChunkerHierarchical uses the hierarchical chunker.
	ChunkerHierarchical ChunkerType = "hierarchical"
)

// ConvertOptions configures optional behaviour for a Convert call.
type ConvertOptions struct {
	// ImageExportMode controls how images appear in the markdown/HTML/JSON output.
	// Defaults to ImageExportModePlaceholder when zero value.
	ImageExportMode ImageExportMode

	// Chunker selects the chunking algorithm. Defaults to ChunkerHybrid.
	Chunker ChunkerType
	// MaxTokens is the maximum number of tokens per chunk. Defaults to 1800.
	MaxTokens int
	// Overlap is the number of overlap tokens between consecutive chunks. Defaults to 200.
	Overlap int
}

// Converter converts documents via Docling.
type Converter interface {
	Convert(ctx context.Context, name string, data []byte, opts ConvertOptions) (*ConvertResult, error)
}

// Client is a real HTTP client for Docling.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Docling client.
func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

// doclingResponse represents the JSON response from Docling's convert endpoint.
type doclingResponse struct {
	Document doclingOutputContent `json:"document"`
	Chunks   []doclingChunk       `json:"chunks"`
	Status   string               `json:"status"`
}

type doclingChunk struct {
	Text  string          `json:"text"`
	Meta  doclingChunkMeta `json:"meta"`
}

type doclingChunkMeta struct {
	Headings []string       `json:"headings"`
	Origin   *doclingOrigin `json:"origin"`
}

type doclingOrigin struct {
	PageStart int `json:"page_start"`
	PageEnd   int `json:"page_end"`
}

type doclingOutputContent struct {
	Markdown string          `json:"md_content"`
	HTML     string          `json:"html_content"`
	JSON     json.RawMessage `json:"json_content"`
}

func (c *Client) Convert(ctx context.Context, name string, data []byte, opts ConvertOptions) (*ConvertResult, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// Add the file.
	filePart, err := w.CreateFormFile("files", name)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := filePart.Write(data); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	// Request markdown, JSON, and HTML output — each format as a separate field.
	for _, format := range []string{"md", "json", "html"} {
		if err := w.WriteField("to_formats", format); err != nil {
			return nil, fmt.Errorf("write to_formats: %w", err)
		}
	}

	// Set image export mode — default to placeholder so base64 blobs don't appear in text output.
	imageMode := opts.ImageExportMode
	if imageMode == "" {
		imageMode = ImageExportModePlaceholder
	}
	if err := w.WriteField("image_export_mode", string(imageMode)); err != nil {
		return nil, fmt.Errorf("write image_export_mode: %w", err)
	}

	// Set chunking parameters.
	chunker := opts.Chunker
	if chunker == "" {
		chunker = ChunkerHybrid
	}
	if err := w.WriteField("chunker", string(chunker)); err != nil {
		return nil, fmt.Errorf("write chunker: %w", err)
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1800
	}
	if err := w.WriteField("max_tokens", fmt.Sprintf("%d", maxTokens)); err != nil {
		return nil, fmt.Errorf("write max_tokens: %w", err)
	}

	overlap := opts.Overlap
	if overlap <= 0 {
		overlap = 200
	}
	if err := w.WriteField("merge_peers", fmt.Sprintf("%d", overlap)); err != nil {
		return nil, fmt.Errorf("write merge_peers: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/convert/file", &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("close docling response body", "error", err)
		}
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docling returned %d: %s", resp.StatusCode, string(respBody))
	}

	var dr doclingResponse
	if err := json.Unmarshal(respBody, &dr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	chunks, err := parseChunks(dr.Chunks)
	if err != nil {
		return nil, fmt.Errorf("parse chunks: %w", err)
	}

	// The full response is the docling JSON we store in the file store.
	return &ConvertResult{
		Markdown:    dr.Document.Markdown,
		HTML:        dr.Document.HTML,
		DoclingJSON: respBody,
		Chunks:      chunks,
	}, nil
}

// LogConverter is a dev stub that logs instead of converting.
type LogConverter struct{}

// NewLogConverter creates a LogConverter.
func NewLogConverter() *LogConverter {
	return &LogConverter{}
}

func (l *LogConverter) Convert(_ context.Context, name string, data []byte, _ ConvertOptions) (*ConvertResult, error) {
	slog.Info("docling: convert (stub)", "name", name, "size", len(data))
	return &ConvertResult{
		Markdown:    fmt.Sprintf("# %s\n\nPlaceholder markdown for document conversion.", name),
		HTML:        fmt.Sprintf("<h1>%s</h1><p>Placeholder HTML for document conversion.</p>", name),
		DoclingJSON: json.RawMessage(`{}`),
	}, nil
}
