package antivirus

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
)

const chunkSize = 2048

// Scanner scans file content for viruses.
type Scanner interface {
	Scan(ctx context.Context, data []byte) (ScanResult, error)
}

// ScanResult holds the outcome of a virus scan.
type ScanResult struct {
	Clean  bool
	Detail string
}

// ClamAVScanner scans files using ClamAV's clamd INSTREAM protocol.
type ClamAVScanner struct {
	address string // clamd TCP address, e.g. "localhost:3310"
}

// NewClamAVScanner creates a ClamAV-backed scanner.
func NewClamAVScanner(address string) *ClamAVScanner {
	return &ClamAVScanner{address: address}
}

func (s *ClamAVScanner) Scan(ctx context.Context, data []byte) (ScanResult, error) {
	slog.Info("antivirus: scanning file via ClamAV",
		"size", len(data),
		"clamd_address", s.address,
	)

	// Connect to clamd.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return ScanResult{}, fmt.Errorf("connect to clamd: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Warn("close clamd connection", "error", err)
		}
	}()

	// Send INSTREAM command (null-terminated).
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return ScanResult{}, fmt.Errorf("send INSTREAM command: %w", err)
	}

	// Stream file in length-prefixed chunks.
	reader := bytes.NewReader(data)
	buf := make([]byte, chunkSize)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			// Write 4-byte big-endian length prefix.
			lenBuf := make([]byte, 4)
			binary.BigEndian.PutUint32(lenBuf, uint32(n))
			if _, err := conn.Write(lenBuf); err != nil {
				return ScanResult{}, fmt.Errorf("write chunk length: %w", err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return ScanResult{}, fmt.Errorf("write chunk data: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ScanResult{}, fmt.Errorf("read chunk: %w", readErr)
		}
	}

	// Send zero-length terminator.
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return ScanResult{}, fmt.Errorf("write terminator: %w", err)
	}

	// Read response.
	var result bytes.Buffer
	if _, err := io.Copy(&result, conn); err != nil {
		return ScanResult{}, fmt.Errorf("read clamd response: %w", err)
	}

	return parseResponse(result.String()), nil
}

func parseResponse(response string) ScanResult {
	response = strings.TrimSpace(response)
	// Strip null terminator if present (clamd protocol).
	response = strings.TrimRight(response, "\x00")

	slog.Info("antivirus: ClamAV response", "response", response)

	// ClamAV INSTREAM protocol responses:
	//   "stream: OK"              — file is clean
	//   "stream: <name> FOUND"    — threat detected
	//   "stream: <error> ERROR"   — scan error

	switch {
	case response == "stream: OK":
		return ScanResult{Clean: true}

	case strings.HasPrefix(response, "stream: ") && strings.HasSuffix(response, " FOUND"):
		detail := strings.TrimPrefix(response, "stream: ")
		detail = strings.TrimSuffix(detail, " FOUND")
		return ScanResult{Clean: false, Detail: detail}

	default:
		return ScanResult{Clean: false, Detail: fmt.Sprintf("unexpected scan response: %s", response)}
	}
}
