package docling

import "strings"

// Chunk represents a semantic chunk produced by Docling's chunking pipeline.
type Chunk struct {
	Text        string `json:"text"`
	HeadingPath string `json:"heading_path"`
	PageStart   int    `json:"page_start"`
	PageEnd     int    `json:"page_end"`
}

// parseChunks converts raw Docling API chunks into the public Chunk type.
func parseChunks(raw []doclingChunk) ([]Chunk, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	chunks := make([]Chunk, 0, len(raw))
	for _, rc := range raw {
		if rc.Text == "" {
			continue
		}

		c := Chunk{Text: rc.Text}

		if len(rc.Meta.Headings) > 0 {
			c.HeadingPath = strings.Join(rc.Meta.Headings, " > ")
		}

		if rc.Meta.Origin != nil {
			c.PageStart = rc.Meta.Origin.PageStart
			c.PageEnd = rc.Meta.Origin.PageEnd
		}

		chunks = append(chunks, c)
	}

	return chunks, nil
}
