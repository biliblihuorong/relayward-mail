package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"relayward-mail/internal/config"
	"relayward-mail/internal/store"
)

// admin implements the "admin" subcommand family. M1 ships create-app so the
// gateway is independently usable before the M2 management API exists.
func admin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: relayward admin create-app NAME [-from ADDR]")
	}
	switch args[0] {
	case "create-app":
		return adminCreateApp(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}

// stringSlice is a repeatable string flag: every -from adds one value.
type stringSlice []string

func (s *stringSlice) String() string { return fmt.Sprint([]string(*s)) }

// Set implements flag.Value.
func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// adminCreateApp creates a sending app locally and prints the one-time SMTP
// password. The default allowed sender matches the plan's NoReply address;
// override with repeatable -from flags.
func adminCreateApp(args []string) error {
	fs := flag.NewFlagSet("admin create-app", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the configuration file")
	var froms stringSlice
	fs.Var(&froms, "from", "allowed sender address (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		return fmt.Errorf("app name is required, usage: relayward admin create-app NAME [-from ADDR]")
	}
	if len(froms) == 0 {
		froms = stringSlice{"NoReply@example.com"}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, dbFileName))
	if err != nil {
		return err
	}
	defer st.Close()

	password, err := store.RandomPassword()
	if err != nil {
		return err
	}
	hash, err := store.HashPassword(password)
	if err != nil {
		return err
	}

	app := &store.App{
		Name:         name,
		PasswordHash: hash,
		Enabled:      true,
		Unsubscribe:  true,
		AllowedFrom:  froms,
		RatePerHour:  500,
	}
	if err := st.CreateApp(context.Background(), app); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("app %q already exists", name)
		}
		return err
	}

	fmt.Printf("app %q created (id %d)\n", app.Name, app.ID)
	fmt.Printf("smtp username: %s\n", app.Name)
	fmt.Printf("smtp password (shown once, store it now): %s\n", password)
	fmt.Printf("allowed senders: %v\n", app.AllowedFrom)
	return nil
}
