package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pocketsearch/internal/api"
	"pocketsearch/internal/search"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	data := flag.String("data", "data/documents.json", "snapshot file; empty for in-memory mode")
	flag.Parse()
	engine, err := search.Open(*data)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: *addr, Handler: api.New(engine),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverError := make(chan error, 1)
	go func() { serverError <- server.ListenAndServe() }()
	slog.Info("starting PocketSearch", "address", *addr, "documents", engine.Stats().Documents)
	select {
	case err := <-serverError:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	err = <-serverError
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
