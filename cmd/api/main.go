// Command api runs the HTTP server for the bet room API.
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
	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
	"github.com/yevheniibutyrin-design/slow-horses/internal/config"
	"github.com/yevheniibutyrin-design/slow-horses/internal/db"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

// sweepInterval is how often unfilled rooms past their expiry are marked
// expired. Reads do not depend on it — expiry is applied lazily on every read —
// so this is housekeeping, and a slow cadence costs nothing.
const sweepInterval = time.Minute

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

	rooms := store.NewRooms(client.Database(cfg.MongoDB))

	indexCtx, cancelIndexes := context.WithTimeout(ctx, 30*time.Second)
	err = rooms.EnsureIndexes(indexCtx)
	cancelIndexes()
	if err != nil {
		// The unique index over a participant's bet reference is what makes a
		// retried create idempotent, so starting without it would quietly allow
		// two rooms for one bet.
		log.Fatalf("could not create room indexes: %v", err)
	}

	verifier := newVerifier(cfg)

	handlers := &api.Handlers{
		Rooms:                rooms,
		OpenAPISpec:          docs.OpenAPI,
		HostEventURLTemplate: cfg.HostEventURLTemplate,
		InviteWindowMS:       cfg.InviteWindowMS,
		SeatHoldTTLMS:        cfg.SeatHoldTTLMS,
		PerDuelMax:           cfg.DuelPerDuelMax,
		DailyLimit:           cfg.DuelDailyLimit,
		Currency:             cfg.DuelCurrency,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.NewRouter(handlers, verifier, cfg.CORSAllowedOrigins),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go sweepExpiredRooms(ctx, rooms)

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

// newVerifier picks how bearer tokens are checked, and says so loudly when they
// are not checked at all.
func newVerifier(cfg config.Config) auth.Verifier {
	if len(cfg.AuthJWTSecret) > 0 {
		log.Print("auth: verifying HS256 bearer tokens")
		return auth.NewHS256Verifier(cfg.AuthJWTSecret)
	}
	log.Print("auth: WARNING — AUTH_JWT_SECRET is not set, so bearer tokens are NOT verified " +
		"and the token itself is taken as the caller's identity. Anyone can impersonate anyone. " +
		"This exists so local development and the smoke script work without a token issuer; " +
		"set AUTH_JWT_SECRET before this service goes anywhere near production.")
	return auth.InsecureVerifier{}
}

// sweepExpiredRooms marks unfilled rooms past their expiry instant as expired
// and drops any hold still on them.
//
// Not load-bearing for correctness: models.Room.EffectiveStatus applies expiry
// on every read, so a room never reads as open past its instant whether or not
// this has run. It keeps stored state honest for anything querying by status.
func sweepExpiredRooms(ctx context.Context, rooms *store.Rooms) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, err := rooms.ExpireStale(sweepCtx, time.Now().UnixMilli())
			cancel()
			if err != nil {
				log.Printf("sweep expired rooms: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("sweep: marked %d room(s) expired", n)
			}
		}
	}
}
