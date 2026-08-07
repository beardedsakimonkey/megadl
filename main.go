// megadl is a TUI for downloading mega.nz links, tracking every
// download and its per-file progress.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"megadl/internal/config"
	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/library"
	"megadl/internal/lockfile"
	"megadl/internal/meganet"
	"megadl/internal/ui"
)

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	drv := &meganet.Driver{}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	// housekeeping. There is no boot recovery to do: what runs next comes from
	// queue membership, which a killed process leaves exactly as it was.
	if _, err := library.Sync(cfg.DownloadDir, database); err != nil {
		return err
	}
	database.PruneTransferLog()

	eng := engine.New(drv, database)
	// Several megadls can watch one library, but only one of them may fetch
	// from it, so the queue is guarded by a lock that lives beside the
	// database. Whoever has something to run takes it; the rest watch.
	eng.SetLock(lockfile.New(filepath.Join(cfg.DownloadDir, ".megadl.lock")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick() // pick up queued downloads from a previous session

	// Read the terminal's colors while stdin is still ours to ask on.
	ui.DetectTheme()

	app := ui.NewApp(cfg, database, eng, drv)
	program := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = program.Run()
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "megadl:", err)
		os.Exit(1)
	}
}
