package fileprocesor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/laenen-partners/entitystore/gen/entitystore/v1/entitystorev1connect"
	"github.com/laenen-partners/fileprocesor/antivirus"
	"github.com/laenen-partners/fileprocesor/docling"
	"github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1/fileprocessorv1connect"
	"github.com/laenen-partners/fileprocesor/gotenberg"
	"github.com/laenen-partners/fileprocesor/pdf2img"
	"github.com/laenen-partners/jobs"
	"github.com/laenen-partners/objectstore"
)

// Closer provides a shutdown hook for the service.
type Closer func()

// New creates a file processor service and returns an http.Handler and a Closer
// that must be called during shutdown to drain DBOS workflows.
func New(cfg Config, store objectstore.Store) (http.Handler, Closer, error) {
	// 0. Validate backend URLs.
	for name, rawURL := range map[string]string{
		"GOTENBERG_URL":    cfg.GotenbergURL,
		"DOCLING_URL":      cfg.DoclingURL,
		"PDF2IMG_URL":      cfg.PDF2ImgURL,
		"ENTITY_STORE_URL": cfg.EntityStoreURL,
	} {
		if rawURL != "" {
			if err := validateBackendURL(name, rawURL); err != nil {
				return nil, nil, err
			}
		}
	}

	// 1. Initialize DBOS.
	dbosCtx, err := dbos.NewDBOSContext(context.Background(), dbos.Config{
		AppName:     "fileprocessor",
		DatabaseURL: cfg.DatabaseURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("init DBOS: %w", err)
	}

	// 2. Initialize jobs client (optional — if EntityStore URL is set).
	var jobsClient *jobs.Client
	if cfg.EntityStoreURL != "" {
		esClient := entitystorev1connect.NewEntityStoreServiceClient(
			&http.Client{Timeout: 30 * time.Second},
			cfg.EntityStoreURL,
		)
		jobsClient = jobs.NewClient(esClient)
		slog.Info("jobs client initialized", "entity_store_url", cfg.EntityStoreURL)
	} else {
		slog.Warn("jobs tracking disabled: ENTITY_STORE_URL not set")
	}

	// 3. Create processor with all deps.
	maxFileSize := cfg.MaxFileSizeBytes
	if maxFileSize <= 0 {
		maxFileSize = 256 << 20 // 256 MB default
	}

	// Build bucket allowlist set.
	var allowedBuckets map[string]bool
	if len(cfg.AllowedBuckets) > 0 {
		allowedBuckets = make(map[string]bool, len(cfg.AllowedBuckets))
		for _, b := range cfg.AllowedBuckets {
			allowedBuckets[b] = true
		}
		slog.Info("bucket allowlist enabled", "buckets", cfg.AllowedBuckets)
	} else {
		slog.Warn("bucket allowlist disabled: ALLOWED_BUCKETS not set (all buckets allowed)")
	}

	proc := &Processor{
		store:          store,
		scanner:        buildScanner(cfg),
		gotenberg:      buildGotenberg(cfg),
		docling:        buildDocling(cfg),
		pdf2img:        buildPdf2img(cfg),
		jobs:           jobsClient,
		dbosCtx:        dbosCtx,
		maxFileSize:    maxFileSize,
		allowedBuckets: allowedBuckets,
	}

	// 4. Register workflows.
	dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)

	// 5. Launch DBOS.
	if err := dbos.Launch(dbosCtx); err != nil {
		return nil, nil, fmt.Errorf("launch DBOS: %w", err)
	}

	// 6. Build connect-go handler with auth interceptor.
	var connectOpts []connect.HandlerOption
	if len(cfg.APIKeys) > 0 {
		connectOpts = append(connectOpts, connect.WithInterceptors(NewAuthInterceptor(cfg.APIKeys)))
	} else {
		slog.Warn("RPC authentication disabled: API_KEYS not set")
	}

	path, rpcHandler := fileprocessorv1connect.NewFileProcessorServiceHandler(
		&Handler{proc: proc},
		connectOpts...,
	)

	// 7. Wire up mux with health endpoints.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.Handle(path, rpcHandler)

	// 8. Apply middleware stack.
	var handler http.Handler = mux
	handler = SecurityHeaders(handler)
	handler = RequestLogging(handler)

	if cfg.RateLimit > 0 {
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = 20
		}
		handler = RateLimit(cfg.RateLimit, burst)(handler)
	}

	if len(cfg.CORSOrigins) > 0 {
		handler = CORS(cfg.CORSOrigins)(handler)
	}

	// 9. Build closer for DBOS cleanup on shutdown.
	closer := func() {
		slog.Info("draining DBOS workflows")
		dbos.Shutdown(dbosCtx, 30*time.Second)
	}

	return handler, closer, nil
}

func buildScanner(cfg Config) antivirus.Scanner {
	if cfg.ClamAVAddr == "" {
		slog.Warn("antivirus scanning disabled: CLAMAV_ADDRESS not set")
		return nil
	}
	return antivirus.NewClamAVScanner(cfg.ClamAVAddr)
}

func buildGotenberg(cfg Config) gotenberg.Converter {
	if cfg.GotenbergURL == "" {
		return gotenberg.NewLogConverter()
	}
	return gotenberg.New(cfg.GotenbergURL)
}

func buildDocling(cfg Config) docling.Converter {
	if cfg.DoclingURL == "" {
		return docling.NewLogConverter()
	}
	return docling.New(cfg.DoclingURL)
}

func buildPdf2img(cfg Config) pdf2img.Converter {
	if cfg.PDF2ImgURL == "" {
		return pdf2img.NewLogConverter()
	}
	return pdf2img.New(cfg.PDF2ImgURL)
}
