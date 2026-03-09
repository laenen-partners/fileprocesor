package pdf2img

import "testing"

func TestFormatToMIME(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"jpg", "image/jpeg"},
		{"jpeg", "image/jpeg"},
		{"png", "image/png"},
		{"webp", "image/webp"},
		{"", "image/jpeg"},
		{"unknown", "image/jpeg"},
	}
	for _, tt := range tests {
		if got := FormatToMIME(tt.format); got != tt.want {
			t.Errorf("FormatToMIME(%q) = %q, want %q", tt.format, got, tt.want)
		}
	}
}

func TestConvertOpts_Defaults(t *testing.T) {
	var opts ConvertOpts

	if got := opts.formatOrDefault(); got != "jpg" {
		t.Errorf("formatOrDefault() = %q, want %q", got, "jpg")
	}
	if got := opts.widthOrDefault(); got != 400 {
		t.Errorf("widthOrDefault() = %d, want 400", got)
	}
	if got := opts.dpiOrDefault(); got != 150 {
		t.Errorf("dpiOrDefault() = %d, want 150", got)
	}
	if got := opts.pageOrDefault(); got != 1 {
		t.Errorf("pageOrDefault() = %d, want 1", got)
	}
}

func TestConvertOpts_Custom(t *testing.T) {
	opts := ConvertOpts{
		Format: "png",
		Width:  800,
		DPI:    300,
		Page:   5,
	}

	if got := opts.formatOrDefault(); got != "png" {
		t.Errorf("formatOrDefault() = %q, want %q", got, "png")
	}
	if got := opts.widthOrDefault(); got != 800 {
		t.Errorf("widthOrDefault() = %d, want 800", got)
	}
	if got := opts.dpiOrDefault(); got != 300 {
		t.Errorf("dpiOrDefault() = %d, want 300", got)
	}
	if got := opts.pageOrDefault(); got != 5 {
		t.Errorf("pageOrDefault() = %d, want 5", got)
	}
}

func TestLogConverter_ConvertPage(t *testing.T) {
	lc := NewLogConverter()
	result, err := lc.ConvertPage(t.Context(), []byte("pdf-data"), ConvertOpts{Format: "png", Page: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Page != 3 {
		t.Errorf("Page = %d, want 3", result.Page)
	}
	if result.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", result.MIMEType)
	}
}

func TestLogConverter_PageCount(t *testing.T) {
	lc := NewLogConverter()
	count, err := lc.PageCount(t.Context(), []byte("pdf-data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("PageCount = %d, want 1", count)
	}
}
