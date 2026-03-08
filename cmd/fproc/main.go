package main

import (
	"log/slog"
	"net/http"
	"os"

	fileprocesor "github.com/laenen-partners/fileprocesor"
	"github.com/laenen-partners/objectstore"
)

func main() {
	objCfg := objectstore.ConfigFromEnv()
	store, err := objectstore.NewStore(objCfg)
	if err != nil {
		slog.Error("failed to create object store", "error", err)
		os.Exit(1)
	}

	cfg := fileprocesor.ConfigFromEnv()
	handler, err := fileprocesor.New(cfg, store)
	if err != nil {
		slog.Error("failed to create file processor", "error", err)
		os.Exit(1)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":3001"
	}

	slog.Info("fileprocessor server starting", "addr", addr, "backend", objCfg.Backend)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
