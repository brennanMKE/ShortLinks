package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/idp"
)

// portikoImport implements `shortlinks portiko-import [--dry-run]` (#0131): it
// copies this instance's users and passkeys into the embedded Portiko provider.
// It reads the same environment as `serve`, so it runs once per instance against
// that instance's own database. With PORTIKO_ENABLED off it prints a note and
// does nothing.
func portikoImport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("portiko-import", flag.ContinueOnError)
	fs.SetOutput(out)
	dryRun := fs.Bool("dry-run", false, "print what would be imported without writing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.PortikoEnabled {
		_, _ = fmt.Fprintln(out, "portiko-import: PORTIKO_ENABLED is off; nothing to do")
		return nil
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := idp.Import(ctx, cfg, pool, *dryRun)
	if err != nil {
		return err
	}
	rep.Print(out, *dryRun)
	if len(rep.Failed) > 0 {
		return fmt.Errorf("portiko-import: %d user(s) failed", len(rep.Failed))
	}
	return nil
}
