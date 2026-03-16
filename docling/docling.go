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

// ConvertOptions configures optional behaviour for a Convert call.
type ConvertOptions struct {
	// ImageExportMode controls how images appear in the markdown/HTML/JSON output.
	// Defaults to ImageExportModePlaceholder when zero value.
	ImageExportMode ImageExportMode
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
	Status   string               `json:"status"`
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

	// The full response is the docling JSON we store in the file store.
	return &ConvertResult{
		Markdown:    dr.Document.Markdown,
		HTML:        dr.Document.HTML,
		DoclingJSON: respBody,
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
