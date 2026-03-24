package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: portforward <gateway>")
		os.Exit(1)
	}
	alias := os.Args[1]

	gw, err := ResolveGateway(alias)
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
