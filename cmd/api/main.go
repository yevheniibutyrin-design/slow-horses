// Command api runs the HTTP server for the rooms API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/yevheniibutyrin-design/slow-horses/docs"
	"github.com/yevheniibutyrin-design/slow-horses/internal/api"
	"github.com/yevheniibutyrin-design/slow-horses/internal/config"
	"github.com/yevheniibutyrin-design/slow-horses/internal/db"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("api: ")

	cfg := config.Load()

	// Signal-aware context: Ctrl-C or `docker compose stop` starts the shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := db.Connect(ctx, cfg.MongoURI)
	if err != nil {
		log.Fatalf("could not reach mongo at %s: %v", cfg.MongoURI, err)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			log.Printf("mongo disconnect: %v", err)
		}
	}()

	handlers := &api.Handlers{
		Rooms:         store.NewRooms(client.Database(cfg.MongoDB)),
		PublicBaseURL: cfg.PublicBaseURL,
		OpenAPISpec:   docs.OpenAPI,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.NewRouter(handlers),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (db %q)", srv.Addr, cfg.MongoDB)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
