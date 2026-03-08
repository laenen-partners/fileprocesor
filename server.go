package fileprocesor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/laenen-partners/fileprocesor/antivirus"
	"github.com/laenen-partners/fileprocesor/docling"
	"github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1/fileprocessorv1connect"
	"github.com/laenen-partners/fileprocesor/gotenberg"
	"github.com/laenen-partners/fileprocesor/pdf2img"
	"github.com/laenen-partners/objectstore"
)

// New creates a file processor service and returns an http.Handler.
func New(cfg Config, store objectstore.Store) (http.Handler, error) {
	// 1. Initialize DBOS.
	dbosCtx, err := dbos.NewDBOSContext(context.Background(), dbos.Config{
		AppName:     "fileprocessor",
		DatabaseURL: cfg.DatabaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("init DBOS: %w", err)
	}

	// 2. Create processor with all deps.
	proc := &Processor{
		store:     store,
		scanner:   buildScanner(cfg),
		gotenberg: buildGotenberg(cfg),
		docling:   buildDocling(cfg),
		pdf2img:   buildPdf2img(cfg),
		dbosCtx:   dbosCtx,
	}

	// 3. Register workflows.
	dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)

	// 4. Launch DBOS.
	if err := dbos.Launch(dbosCtx); err != nil {
		return nil, fmt.Errorf("launch DBOS: %w", err)
	}

	// 5. Mount connect-go handler.
	path, rpcHandler := fileprocessorv1connect.NewFileProcessorServiceHandler(
		&Handler{proc: proc},
	)
	mux := http.NewServeMux()
	mux.Handle(path, rpcHandler)
	return mux, nil
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
