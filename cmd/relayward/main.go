// Command relayward is the mail gateway entrypoint. Subcommands:
//
//	relayward serve -config config.yaml
//	relayward admin create-app NAME [-from addr]...
//	relayward healthcheck [-config config.yaml] [-url URL]
package main

import (
	"fmt"
	"os"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "serve":
		err = serve(args[1:])
	case "admin":
		err = admin(args[1:])
	case "healthcheck":
		err = healthcheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: relayward <command> [flags]

commands:
  serve                       run the gateway (SMTP, public and admin listeners)
  admin create-app NAME       create a sending app, print its one-time SMTP password
  healthcheck                 GET /healthz and exit 0 on success (for Docker HEALTHCHECK)
`)
}
