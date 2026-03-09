package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	handler, closer, err := fileprocesor.New(cfg, store)
	if err != nil {
		slog.Error("failed to create file processor", "error", err)
		os.Exit(1)
	}
	defer closer()

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":3001"
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("fileprocessor server starting", "addr", addr, "backend", objCfg.Backend)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gracefully")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "server stopped")
}
