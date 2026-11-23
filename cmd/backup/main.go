// backup writes the whole deployment's backup (docs/backup.md): every account, with its password hash and linked sign-ins, and everything of it nothing can rebuild from its files.
//
// In the cluster: kubectl -n taiga exec deploy/taiga-backend -- backup > taiga.tar
// It never migrates the database, so run it with the image that's deployed, before a new one migrates anything.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
)

func main() {
	output := flag.String("o", "", "write the backup here instead of to standard output; it appears under this name only once it's whole")
	fetched := flag.Bool("fetched", true, "include weather and COROS's figures, which can be fetched again")
	flag.Parse()

	cfg, err := config.Load("config")
	if err != nil {
		log.Fatalf("loading configuration: %v", err)
	}
	// A backup taken before an upgrade must be of the schema as it is.
	cfg.Db.AutoMigrateUp = false
	cfg.Db.DropOnStart = false
	m, err := models.New(cfg)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer func() { _ = m.Close() }()
	repo, err := filerepo.NewFileRepo(*cfg)
	if err != nil {
		log.Fatalf("opening file repo: %v", err)
	}

	ctx := context.Background()
	a, err := m.ExportDeployment(ctx, *fetched, time.Now())
	if err != nil {
		log.Fatalf("%v", err)
	}
	files := len(a.AllFiles())
	write := func(w io.Writer) error { return backup.Write(ctx, w, a, repo.Fetch) }
	if *output == "" {
		if err := write(os.Stdout); err != nil {
			log.Fatalf("writing the backup: %v", err)
		}
	} else if err := writeFile(*output, write); err != nil {
		log.Fatalf("writing the backup: %v", err)
	}
	fmt.Fprintf(os.Stderr, "backed up %d accounts and %d files\n", len(a.Accounts), files)
}

// writeFile writes beside path and renames into place, so a failed backup never replaces a good one.
func writeFile(path string, write func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
