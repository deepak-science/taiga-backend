// restore reads a deployment's backup (docs/backup.md) into an empty deployment: the accounts, their files, and what can't be rebuilt from them, with every workout made again from its file.
//
// restore -i taiga.tar, or from standard input. It refuses a deployment that already has an account.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
)

func main() {
	input := flag.String("i", "", "read the backup from here instead of standard input")
	flag.Parse()

	var in io.Reader = os.Stdin
	if *input != "" {
		f, err := os.Open(*input)
		if err != nil {
			log.Fatalf("%v", err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}

	cfg, err := config.Load("config")
	if err != nil {
		log.Fatalf("loading configuration: %v", err)
	}
	m, err := models.New(cfg)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer func() { _ = m.Close() }()
	repo, err := filerepo.NewFileRepo(*cfg)
	if err != nil {
		log.Fatalf("opening file repo: %v", err)
	}
	if err := run(context.Background(), m, repo, in, os.Stderr); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(ctx context.Context, m models.Model, repo filerepo.FileRepo, in io.Reader, out io.Writer) error {
	if err := m.CheckEmpty(ctx); err != nil {
		return err
	}
	// The files go into the repo as they're read; they're content-addressed, so a restore that fails later leaves nothing wrong behind there.
	a, err := backup.Read(in, func(hash string, content []byte) error {
		stored, err := repo.Store(ctx, bytes.NewReader(content))
		if err != nil {
			return fmt.Errorf("storing file %s: %w", hash, err)
		}
		if stored != hash {
			return fmt.Errorf("file %s was stored as %s", hash, stored)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if a.Manifest.Kind != backup.KindDeployment {
		return fmt.Errorf("that's one user's export, not a deployment's backup; import it into an account instead")
	}
	now := time.Now()
	for i := range a.Accounts {
		acct := &a.Accounts[i]
		report, err := m.RestoreAccount(ctx, acct, repo, now)
		if err != nil {
			return fmt.Errorf("restoring %s: %w (the deployment is part-restored: empty it before trying again)", acct.Email, err)
		}
		_, _ = fmt.Fprintf(out, "restored %s as account %d: %d files, %d workouts\n", acct.Email, report.UserID, len(acct.Files), report.Workouts)
		for hash, why := range report.Unreadable {
			_, _ = fmt.Fprintf(out, "  kept %s as a file only: %s\n", hash, why)
		}
	}
	_, _ = fmt.Fprintln(out, "route detection is queued for each account; the server's worker runs it")
	return nil
}
