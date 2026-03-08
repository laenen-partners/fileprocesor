package antivirus

import (
	"context"
	"log/slog"
)

// LogScanner always returns clean. Useful for development and testing.
type LogScanner struct{}

// NewLogScanner creates a log-based scanner stub.
func NewLogScanner() *LogScanner {
	return &LogScanner{}
}

func (s *LogScanner) Scan(_ context.Context, data []byte) (ScanResult, error) {
	slog.Info("antivirus: scan completed (log stub)",
		"size", len(data),
		"result", "CLEAN",
	)
	return ScanResult{Clean: true}, nil
}
