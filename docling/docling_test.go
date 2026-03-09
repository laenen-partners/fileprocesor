package docling

import "testing"

func TestLogConverter_Convert(t *testing.T) {
	lc := NewLogConverter()
	result, err := lc.Convert(t.Context(), "test.pdf", []byte("pdf-data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Markdown == "" {
		t.Error("expected non-empty Markdown from log converter")
	}
	if result.HTML == "" {
		t.Error("expected non-empty HTML from log converter")
	}
	if result.DoclingJSON == nil {
		t.Error("expected non-nil DoclingJSON from log converter")
	}
}
