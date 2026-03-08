package fileprocesor

import "os"

// Config holds settings for the file processor service.
type Config struct {
	DatabaseURL  string // PostgreSQL for DBOS system DB (required)
	GotenbergURL string // empty = log stub
	DoclingURL   string // empty = log stub
	PDF2ImgURL   string // empty = log stub
	ClamAVAddr   string // empty = skip scanning
}

// ConfigFromEnv reads configuration from environment variables.
func ConfigFromEnv() Config {
	return Config{
		DatabaseURL:  os.Getenv("DBOS_DATABASE_URL"),
		GotenbergURL: os.Getenv("GOTENBERG_URL"),
		DoclingURL:   os.Getenv("DOCLING_URL"),
		PDF2ImgURL:   os.Getenv("PDF2IMG_URL"),
		ClamAVAddr:   os.Getenv("CLAMAV_ADDRESS"),
	}
}
