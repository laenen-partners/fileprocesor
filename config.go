package fileprocesor

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds settings for the file processor service.
type Config struct {
	DatabaseURL    string // PostgreSQL for DBOS system DB (required)
	GotenbergURL   string // empty = log stub
	DoclingURL     string // empty = log stub
	PDF2ImgURL     string // empty = log stub
	ClamAVAddr     string // empty = skip scanning
	EntityStoreURL string // EntityStore service URL (required for jobs)

	// Security
	APIKeys        []string // comma-separated API keys for RPC auth
	AllowedBuckets []string // allowed bucket names (empty = allow all)
	RateLimit      float64  // requests per second per IP (0 = disabled)
	RateBurst      int      // burst allowance per IP
	CORSOrigins    []string // allowed CORS origins

	// Limits
	MaxFileSizeBytes int64 // max file size for downloads (0 = 256MB default)
}

// ConfigFromEnv reads configuration from environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		DatabaseURL:    os.Getenv("DBOS_DATABASE_URL"),
		GotenbergURL:   os.Getenv("GOTENBERG_URL"),
		DoclingURL:     os.Getenv("DOCLING_URL"),
		PDF2ImgURL:     os.Getenv("PDF2IMG_URL"),
		ClamAVAddr:     os.Getenv("CLAMAV_ADDRESS"),
		EntityStoreURL: os.Getenv("ENTITY_STORE_URL"),
	}

	if keys := os.Getenv("API_KEYS"); keys != "" {
		cfg.APIKeys = strings.Split(keys, ",")
	}

	if v := os.Getenv("RATE_LIMIT"); v != "" {
		cfg.RateLimit, _ = strconv.ParseFloat(v, 64)
	}
	if v := os.Getenv("RATE_BURST"); v != "" {
		cfg.RateBurst, _ = strconv.Atoi(v)
	}

	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		cfg.CORSOrigins = strings.Split(origins, ",")
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
