package docling

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogConverter_Convert(t *testing.T) {
	lc := NewLogConverter()
	result, err := lc.Convert(t.Context(), "test.pdf", []byte("pdf-data"), ConvertOptions{})
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

func TestClient_Convert_Chunks(t *testing.T) {
	// Fake Docling server that returns a response with chunks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify chunking fields are sent.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("chunker"); got != "hybrid" {
			http.Error(w, fmt.Sprintf("expected chunker=hybrid, got %q", got), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("max_tokens"); got != "1800" {
			http.Error(w, fmt.Sprintf("expected max_tokens=1800, got %q", got), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("merge_peers"); got != "200" {
			http.Error(w, fmt.Sprintf("expected merge_peers=200, got %q", got), http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"status": "ok",
			"document": map[string]any{
				"md_content":   "# Hello",
				"html_content": "<h1>Hello</h1>",
				"json_content": map[string]any{},
			},
			"chunks": []map[string]any{
				{
					"text": "First chunk text",
					"meta": map[string]any{
						"headings": []string{"Introduction", "Overview"},
						"origin":   map[string]any{"page_start": 1, "page_end": 2},
					},
				},
				{
					"text": "Second chunk text",
					"meta": map[string]any{
						"headings": []string{"Details"},
						"origin":   map[string]any{"page_start": 3, "page_end": 3},
					},
				},
				{
					"text": "",
					"meta": map[string]any{},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := New(srv.URL)
	result, err := client.Convert(context.Background(), "test.pdf", []byte("fake-pdf"), ConvertOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Markdown != "# Hello" {
		t.Errorf("unexpected markdown: %q", result.Markdown)
	}

	if len(result.Chunks) != 2 {
		t.Fatalf("expected 2 chunks (empty skipped), got %d", len(result.Chunks))
	}

	c0 := result.Chunks[0]
	if c0.Text != "First chunk text" {
		t.Errorf("chunk[0].Text = %q, want %q", c0.Text, "First chunk text")
	}
	if c0.HeadingPath != "Introduction > Overview" {
		t.Errorf("chunk[0].HeadingPath = %q, want %q", c0.HeadingPath, "Introduction > Overview")
	}
	if c0.PageStart != 1 {
		t.Errorf("chunk[0].PageStart = %d, want 1", c0.PageStart)
	}
	if c0.PageEnd != 2 {
		t.Errorf("chunk[0].PageEnd = %d, want 2", c0.PageEnd)
	}

	c1 := result.Chunks[1]
	if c1.Text != "Second chunk text" {
		t.Errorf("chunk[1].Text = %q, want %q", c1.Text, "Second chunk text")
	}
	if c1.PageStart != 3 || c1.PageEnd != 3 {
		t.Errorf("chunk[1] pages = %d-%d, want 3-3", c1.PageStart, c1.PageEnd)
	}
}

func TestClient_Convert_CustomChunkOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("chunker"); got != "hierarchical" {
			http.Error(w, fmt.Sprintf("expected chunker=hierarchical, got %q", got), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("max_tokens"); got != "500" {
			http.Error(w, fmt.Sprintf("expected max_tokens=500, got %q", got), http.StatusBadRequest)
			return
		}
		if got := r.FormValue("merge_peers"); got != "50" {
			http.Error(w, fmt.Sprintf("expected merge_peers=50, got %q", got), http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"status": "ok",
			"document": map[string]any{
				"md_content":   "# Doc",
				"html_content": "<h1>Doc</h1>",
				"json_content": map[string]any{},
			},
			"chunks": []map[string]any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := New(srv.URL)
	result, err := client.Convert(context.Background(), "doc.pdf", []byte("data"), ConvertOptions{
		Chunker:   ChunkerHierarchical,
		MaxTokens: 500,
		Overlap:   50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(result.Chunks))
	}
}

func TestClient_Convert_NoChunksInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"status": "ok",
			"document": map[string]any{
				"md_content":   "# Doc",
				"html_content": "<h1>Doc</h1>",
				"json_content": map[string]any{},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := New(srv.URL)
	result, err := client.Convert(context.Background(), "doc.pdf", []byte("data"), ConvertOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Chunks != nil {
		t.Errorf("expected nil chunks, got %v", result.Chunks)
	}
}

func TestClient_Convert_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error")
	}))
	defer srv.Close()

	client := New(srv.URL)
	_, err := client.Convert(context.Background(), "doc.pdf", []byte("data"), ConvertOptions{})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestParseChunks(t *testing.T) {
	raw := []doclingChunk{
		{
			Text: "chunk one",
			Meta: doclingChunkMeta{
				Headings: []string{"A", "B"},
				Origin:   &doclingOrigin{PageStart: 1, PageEnd: 1},
			},
		},
		{Text: ""}, // skipped
		{
			Text: "chunk two",
			Meta: doclingChunkMeta{
				Origin: &doclingOrigin{PageStart: 2, PageEnd: 3},
			},
		},
		{
			Text: "chunk three no origin",
			Meta: doclingChunkMeta{Headings: []string{"C"}},
		},
	}

	chunks, err := parseChunks(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].HeadingPath != "A > B" {
		t.Errorf("chunks[0].HeadingPath = %q, want %q", chunks[0].HeadingPath, "A > B")
	}
	if chunks[1].HeadingPath != "" {
		t.Errorf("chunks[1].HeadingPath = %q, want empty", chunks[1].HeadingPath)
	}
	if chunks[2].PageStart != 0 || chunks[2].PageEnd != 0 {
		t.Errorf("chunks[2] pages = %d-%d, want 0-0 (no origin)", chunks[2].PageStart, chunks[2].PageEnd)
	}
}

func TestParseChunks_Nil(t *testing.T) {
	chunks, err := parseChunks(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil, got %v", chunks)
	}
}
