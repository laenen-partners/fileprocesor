package gotenberg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
)

// NamedPDF represents a named PDF for merging.
type NamedPDF struct {
	Name string
	Data []byte
}

// Converter converts documents to PDF and merges PDFs.
type Converter interface {
	ConvertToPDF(ctx context.Context, name string, data []byte, contentType string) ([]byte, error)
	MergePDFs(ctx context.Context, pdfs []NamedPDF) ([]byte, error)
}

// Client is a real HTTP client for Gotenberg.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Gotenberg client.
func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{},
	}
}

func (c *Client) ConvertToPDF(ctx context.Context, name string, data []byte, contentType string) ([]byte, error) {
	// Images → wrap in HTML and use Chromium route.
	if isImage(contentType) {
		return c.convertImageToPDF(ctx, name, data, contentType)
	}
	// PDF passthrough.
	if contentType == "application/pdf" {
		return data, nil
	}
	// Everything else → LibreOffice.
	return c.convertViaLibreOffice(ctx, name, data)
}

func (c *Client) convertImageToPDF(ctx context.Context, name string, data []byte, contentType string) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// Write an HTML file that contains the image as a base64 data URI.
	htmlPart, err := w.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("create html part: %w", err)
	}
	// Gotenberg's Chromium route can reference local files by name.
	html := `<!DOCTYPE html>
<html><body style="margin:0;padding:0;">
<img src="` + name + `" style="max-width:100%;height:auto;">
</body></html>`
	if _, err := htmlPart.Write([]byte(html)); err != nil {
		return nil, fmt.Errorf("write html: %w", err)
	}

	// Attach the image file.
	imgPart, err := w.CreateFormFile("files", name)
	if err != nil {
		return nil, fmt.Errorf("create image part: %w", err)
	}
	if _, err := imgPart.Write(data); err != nil {
		return nil, fmt.Errorf("write image: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	return c.doRequest(ctx, "/forms/chromium/convert/html", w.FormDataContentType(), &body)
}

func (c *Client) convertViaLibreOffice(ctx context.Context, name string, data []byte) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	part, err := w.CreateFormFile("files", name)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	return c.doRequest(ctx, "/forms/libreoffice/convert", w.FormDataContentType(), &body)
}

func (c *Client) MergePDFs(ctx context.Context, pdfs []NamedPDF) ([]byte, error) {
	if len(pdfs) == 1 {
		return pdfs[0].Data, nil
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	for i, pdf := range pdfs {
		name := fmt.Sprintf("%03d.pdf", i)
		part, err := w.CreateFormFile("files", name)
		if err != nil {
			return nil, fmt.Errorf("create pdf part %d: %w", i, err)
		}
		if _, err := part.Write(pdf.Data); err != nil {
			return nil, fmt.Errorf("write pdf %d: %w", i, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	return c.doRequest(ctx, "/forms/pdfengines/merge", w.FormDataContentType(), &body)
}

func (c *Client) doRequest(ctx context.Context, path, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("close gotenberg response body", "error", err)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gotenberg returned %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func isImage(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}

// LogConverter is a dev stub that logs instead of converting.
type LogConverter struct{}

// NewLogConverter creates a LogConverter.
func NewLogConverter() *LogConverter {
	return &LogConverter{}
}

func (l *LogConverter) ConvertToPDF(_ context.Context, name string, data []byte, contentType string) ([]byte, error) {
	slog.Info("gotenberg: convert to PDF (stub)", "name", name, "content_type", contentType, "size", len(data))
	if contentType == "application/pdf" {
		return data, nil
	}
	// Return a minimal valid PDF.
	return minimalPDF(), nil
}

func (l *LogConverter) MergePDFs(_ context.Context, pdfs []NamedPDF) ([]byte, error) {
	slog.Info("gotenberg: merge PDFs (stub)", "count", len(pdfs))
	if len(pdfs) > 0 {
		return pdfs[0].Data, nil
	}
	return minimalPDF(), nil
}

func minimalPDF() []byte {
	return []byte(`%PDF-1.0
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R>>endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer<</Size 4/Root 1 0 R>>
startxref
190
%%EOF`)
}
