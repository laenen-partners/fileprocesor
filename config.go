package fileprocesor

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds settings for the file processor.
type Config struct {
	GotenbergURL string // empty = log stub
	DoclingURL   string // empty = log stub
	PDF2ImgURL   string // empty = log stub
	ClamAVAddr   string // empty = skip scanning

	// Security
	AllowedBuckets []string // allowed bucket names (empty = allow all)

	// Limits
	MaxFileSizeBytes int64 // max file size for downloads (0 = 256MB default)
}

// ConfigFromEnv reads configuration from environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		GotenbergURL: os.Getenv("GOTENBERG_URL"),
		DoclingURL:   os.Getenv("DOCLING_URL"),
		PDF2ImgURL:   os.Getenv("PDF2IMG_URL"),
		ClamAVAddr:   os.Getenv("CLAMAV_ADDRESS"),
	}

	if buckets := os.Getenv("ALLOWED_BUCKETS"); buckets != "" {
		cfg.AllowedBuckets = strings.Split(buckets, ",")
	}

	if v := os.Getenv("MAX_FILE_SIZE_BYTES"); v != "" {
		cfg.MaxFileSizeBytes, _ = strconv.ParseInt(v, 10, 64)
	}

	return cfg
}

// validateBackendURL validates that a backend URL has an http(s) scheme and a host.
func validateBackendURL(name, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s: invalid URL %q: %w", name, rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: URL %q must use http or https scheme", name, rawURL)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: URL %q has no host", name, rawURL)
	}
	return nil
}
