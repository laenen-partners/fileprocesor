package gotenberg

import "testing"

func TestIsImage(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/webp", true},
		{"image/svg+xml", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isImage(tt.ct); got != tt.want {
			t.Errorf("isImage(%q) = %v, want %v", tt.ct, got, tt.want)
		}
	}
}

func TestConvertToPDF_PDFPassthrough(t *testing.T) {
	c := New("http://localhost:3100")
	data := []byte("%PDF-1.0 test data")
	result, err := c.ConvertToPDF(t.Context(), "test.pdf", data, "application/pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(data) {
		t.Error("PDF passthrough should return original data")
	}
}

func TestMinimalPDF(t *testing.T) {
	pdf := minimalPDF()
	if len(pdf) == 0 {
		t.Error("minimalPDF should not be empty")
	}
	if string(pdf[:5]) != "%PDF-" {
		t.Error("minimalPDF should start with %PDF-")
	}
}

func TestLogConverter_ConvertToPDF(t *testing.T) {
	lc := NewLogConverter()

	// PDF passthrough.
	pdfData := []byte("%PDF-test")
	result, err := lc.ConvertToPDF(t.Context(), "doc.pdf", pdfData, "application/pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(pdfData) {
		t.Error("log converter should pass through PDF data")
	}

	// Non-PDF returns minimal PDF.
	result, err = lc.ConvertToPDF(t.Context(), "doc.txt", []byte("hello"), "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result[:5]) != "%PDF-" {
		t.Error("log converter should return minimal PDF for non-PDF input")
	}
}

func TestLogConverter_MergePDFs(t *testing.T) {
	lc := NewLogConverter()

	// With PDFs — returns first.
	first := []byte("first-pdf")
	result, err := lc.MergePDFs(t.Context(), []NamedPDF{
		{Name: "a.pdf", Data: first},
		{Name: "b.pdf", Data: []byte("second")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(first) {
		t.Error("merge stub should return first PDF data")
	}

	// Empty — returns minimal PDF.
	result, err = lc.MergePDFs(t.Context(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result[:5]) != "%PDF-" {
		t.Error("merge stub with no PDFs should return minimal PDF")
	}
}
