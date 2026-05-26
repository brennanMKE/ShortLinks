package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
)

const version = "0.1.0"

func main() {
	// Subcommand routing: `shortlinks serve` starts the HTTP server;
	// `shortlinks seed` bootstraps the admin user and a test link; anything
	// else (including no argument or `version`) prints the version.
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			log.Fatalf("shortlinks: %v", err)
		}
	case "seed":
		if err := seed(); err != nil {
			log.Fatalf("shortlinks: %v", err)
		}
	default:
		fmt.Printf("shortlinks %s\n", version)
	}
}

// serve loads configuration, connects the database pool, mounts the routes, and
// listens on the configured port until the process is terminated.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.Handle("GET /health", handlers.NewHealthHandler(pool))

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("shortlinks %s listening on %s", version, addr)
	return http.ListenAndServe(addr, mux)
}
