package pdf2img

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// maxResponseSize is the maximum size of a pdf2img response body (256 MB).
const maxResponseSize = 256 << 20

// ConvertOpts configures a single-page conversion.
type ConvertOpts struct {
	Format string // "jpg", "png", "webp" (default "jpg")
	Width  int    // pixel width (default 400)
	DPI    int    // density (default 150)
	Page   int    // 1-based page number (default 1)
}

func (o ConvertOpts) formatOrDefault() string {
	if o.Format == "" {
		return "jpg"
	}
	return o.Format
}

func (o ConvertOpts) widthOrDefault() int {
	if o.Width <= 0 {
		return 400
	}
	return o.Width
}

func (o ConvertOpts) dpiOrDefault() int {
	if o.DPI <= 0 {
		return 150
	}
	return o.DPI
}

func (o ConvertOpts) pageOrDefault() int {
	if o.Page <= 0 {
		return 1
	}
	return o.Page
}

// PageResult is the output of a single-page conversion.
type PageResult struct {
	Page     int
	Data     []byte
	MIMEType string
}

// Converter generates thumbnails from PDF data.
type Converter interface {
	GenerateThumbnail(ctx context.Context, pdfData []byte) ([]byte, error)
	ConvertPage(ctx context.Context, pdfData []byte, opts ConvertOpts) (*PageResult, error)
	PageCount(ctx context.Context, pdfData []byte) (int, error)
}

// Client is a real HTTP client for pdf2img.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a pdf2img client.
func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *Client) GenerateThumbnail(ctx context.Context, pdfData []byte) ([]byte, error) {
	result, err := c.ConvertPage(ctx, pdfData, ConvertOpts{})
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

func (c *Client) ConvertPage(ctx context.Context, pdfData []byte, opts ConvertOpts) (*PageResult, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	filePart, err := w.CreateFormFile("file", "document.pdf")
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := filePart.Write(pdfData); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	format := opts.formatOrDefault()
	for _, field := range []struct{ k, v string }{
		{"format", format},
		{"density", strconv.Itoa(opts.dpiOrDefault())},
		{"width", strconv.Itoa(opts.widthOrDefault())},
		{"page", strconv.Itoa(opts.pageOrDefault())},
	} {
		if err := w.WriteField(field.k, field.v); err != nil {
			return nil, fmt.Errorf("write field %s: %w", field.k, err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/convert", &body)
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
			slog.Error("close pdf2img response body", "error", err)
		}
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pdf2img returned %d: %s", resp.StatusCode, string(respBody))
	}

	return &PageResult{
		Page:     opts.pageOrDefault(),
		Data:     respBody,
		MIMEType: FormatToMIME(format),
	}, nil
}

func (c *Client) PageCount(_ context.Context, pdfData []byte) (int, error) {
	r := bytes.NewReader(pdfData)
	n, err := api.PageCount(r, nil)
	if err != nil {
		return 0, fmt.Errorf("count pages: %w", err)
	}
	return n, nil
}

// FormatToMIME returns the MIME type for a given image format string.
func FormatToMIME(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// LogConverter is a dev stub that logs instead of converting.
type LogConverter struct{}

// NewLogConverter creates a LogConverter.
func NewLogConverter() *LogConverter {
	return &LogConverter{}
}

func (l *LogConverter) GenerateThumbnail(_ context.Context, pdfData []byte) ([]byte, error) {
	slog.Info("pdf2img: generate thumbnail (stub)", "pdf_size", len(pdfData))
	return nil, nil
}

func (l *LogConverter) ConvertPage(_ context.Context, pdfData []byte, opts ConvertOpts) (*PageResult, error) {
	slog.Info("pdf2img: convert page (stub)", "pdf_size", len(pdfData), "page", opts.pageOrDefault(), "format", opts.formatOrDefault())
	return &PageResult{
		Page:     opts.pageOrDefault(),
		MIMEType: FormatToMIME(opts.formatOrDefault()),
	}, nil
}

func (l *LogConverter) PageCount(_ context.Context, pdfData []byte) (int, error) {
	slog.Info("pdf2img: page count (stub)", "pdf_size", len(pdfData))
	return 1, nil
}
