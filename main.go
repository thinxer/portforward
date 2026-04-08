package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type options struct {
	gateway   string
	proxyJump string
}

func parseArgs(args []string) (options, error) {
	var opts options

	fs := flag.NewFlagSet("portforward", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.proxyJump, "J", "", "jump hosts")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 1 {
		return options{}, fmt.Errorf("expected exactly one gateway")
	}

	opts.gateway = fs.Arg(0)
	return opts, nil
}

func usage() string {
	return "usage: portforward [-J jump[,jump...]] <gateway>"
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, usage())
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	alias := opts.gateway

	gw, err := ResolveGateway(alias, opts.proxyJump)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ssh config:", err)
		os.Exit(1)
	}

	cfg, err := LoadConfig(alias)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	mgr, err := NewManager(gw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	m := NewModel(alias, mgr, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
