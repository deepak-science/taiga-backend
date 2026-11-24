// admin grants or revokes the admin role, which the API can't, so the first admin is made from inside the deployment.
//
// In the cluster: kubectl -n taiga exec deploy/taiga-backend -- admin -grant you@example.com
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/models"
)

func main() {
	grant := flag.String("grant", "", "give the account with this email the admin role")
	revoke := flag.String("revoke", "", "take the admin role from the account with this email")
	list := flag.Bool("list", false, "list the accounts that hold the role")
	flag.Parse()

	set := 0
	for _, on := range []bool{*grant != "", *revoke != "", *list} {
		if on {
			set++
		}
	}
	if set != 1 || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "use exactly one of -grant, -revoke or -list")
		flag.Usage()
		os.Exit(2)
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

	if err := run(context.Background(), m, models.NewAdmins(cfg.Auth.Admins), *grant, *revoke, *list); err != nil {
		_ = m.Close()
		log.Fatal(err)
	}
}

func run(ctx context.Context, m models.Model, admins models.Admins, grant, revoke string, list bool) error {
	switch {
	case grant != "":
		u, err := m.SetAdmin(ctx, grant, true)
		if err != nil {
			return describe(grant, err)
		}
		fmt.Printf("user %d (%s) is an admin\n", u.ID, u.Email)
	case revoke != "":
		u, err := m.SetAdmin(ctx, revoke, false)
		if err != nil {
			return describe(revoke, err)
		}
		fmt.Printf("user %d (%s) is no longer an admin\n", u.ID, u.Email)
		// auth.admins grants the role without a row to clear, so revoking can't take it from a listed account.
		if st, err := m.AdminStatus(ctx, u.ID); err == nil && admins.Is(st) {
			fmt.Printf("but auth.admins still lists %s, so it keeps the role until that changes\n", u.Email)
		}
	case list:
		accounts, err := m.AdminAccounts(ctx, admins)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if a.Admin {
				fmt.Printf("%d\t%s\n", a.ID, a.Email)
			}
		}
	}
	return nil
}

func describe(email string, err error) error {
	if errors.Is(err, models.ErrNoSuchAccount) {
		return fmt.Errorf("%s: %w; create it with useradd, or check the case", email, err)
	}
	return fmt.Errorf("%s: %w", email, err)
}
