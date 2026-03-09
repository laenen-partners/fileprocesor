package antivirus

import "testing"

func TestParseResponse_Clean(t *testing.T) {
	r := parseResponse("stream: OK")
	if !r.Clean {
		t.Error("expected Clean=true")
	}
	if r.Detail != "" {
		t.Errorf("expected empty Detail, got %q", r.Detail)
	}
}

func TestParseResponse_CleanWithNull(t *testing.T) {
	r := parseResponse("stream: OK\x00")
	if !r.Clean {
		t.Error("expected Clean=true after null-stripping")
	}
}

func TestParseResponse_VirusFound(t *testing.T) {
	r := parseResponse("stream: Eicar-Signature FOUND")
	if r.Clean {
		t.Error("expected Clean=false")
	}
	if r.Detail != "Eicar-Signature" {
		t.Errorf("Detail = %q, want %q", r.Detail, "Eicar-Signature")
	}
}

func TestParseResponse_VirusFound_MultiWord(t *testing.T) {
	r := parseResponse("stream: Win.Trojan.Generic-12345 FOUND")
	if r.Clean {
		t.Error("expected Clean=false")
	}
	if r.Detail != "Win.Trojan.Generic-12345" {
		t.Errorf("Detail = %q, want %q", r.Detail, "Win.Trojan.Generic-12345")
	}
}

func TestParseResponse_GarbledOK(t *testing.T) {
	// Must NOT match — garbled response should be treated as error.
	r := parseResponse("garbled OK")
	if r.Clean {
		t.Error("garbled response should not be treated as clean")
	}
}

func TestParseResponse_Error(t *testing.T) {
	r := parseResponse("stream: INSTREAM size limit exceeded ERROR")
	if r.Clean {
		t.Error("expected Clean=false on error")
	}
	if r.Detail == "" {
		t.Error("expected non-empty Detail on error")
	}
}

func TestParseResponse_Empty(t *testing.T) {
	r := parseResponse("")
	if r.Clean {
		t.Error("empty response should not be clean")
	}
}

func TestParseResponse_Whitespace(t *testing.T) {
	r := parseResponse("  stream: OK  \n")
	if !r.Clean {
		t.Error("expected Clean=true after whitespace trim")
	}
}
