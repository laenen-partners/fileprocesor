package docling

import (
	"encoding/json"
	"fmt"
)

// Chunk represents a semantic chunk extracted from a DoclingDocument.
type Chunk struct {
	Text       string
	Heading    string
	Type       string
	PageNumber int
	Index      int
}

// doclingDocument is a minimal representation of the DoclingDocument JSON
// for extracting chunks. We only parse the fields we need.
type doclingDocument struct {
	Texts []doclingText `json:"texts"`
}

type doclingText struct {
	Text     string       `json:"text"`
	Label    string       `json:"label"`
	Prov     []doclingRef `json:"prov"`
	Headings []string     `json:"headings"`
}

type doclingRef struct {
	PageNo int `json:"page_no"`
}

// ParseChunks extracts chunks from a DoclingDocument JSON payload.
// Returns an empty slice (not error) if the JSON has no recognisable text elements.
func ParseChunks(doclingJSON json.RawMessage) ([]Chunk, error) {
	if len(doclingJSON) == 0 || string(doclingJSON) == "{}" || string(doclingJSON) == "null" {
		return nil, nil
	}

	var doc doclingDocument
	if err := json.Unmarshal(doclingJSON, &doc); err != nil {
		return nil, fmt.Errorf("parse docling document: %w", err)
	}

	chunks := make([]Chunk, 0, len(doc.Texts))
	for i, t := range doc.Texts {
		if t.Text == "" {
			continue
		}

		pageNo := 0
		if len(t.Prov) > 0 {
			pageNo = t.Prov[0].PageNo
		}

		heading := ""
		if len(t.Headings) > 0 {
			heading = t.Headings[len(t.Headings)-1]
		}

		chunks = append(chunks, Chunk{
			Text:       t.Text,
			Heading:    heading,
			Type:       t.Label,
			PageNumber: pageNo,
			Index:      i,
		})
	}

	return chunks, nil
}
