package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── styles ────────────────────────────────────────────────────────────────────

var (
	styleHeader  = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	styleDivider = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	styleListening  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // bright green
	styleConnecting = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))  // bright yellow
	styleError      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // bright red
	styleDisabled   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // gray
	styleConnAddr   = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // bright blue

	styleInputPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleInputError  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	stylePaneTitle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			PaddingLeft(1)
)

// ── list item ─────────────────────────────────────────────────────────────────

type forwardItem struct {
	idx   int
	spec  ForwardSpec
	state TunnelState
	err   error
}

func (f forwardItem) Title() string {
	var status string
	switch f.state {
	case StateListening:
		status = styleListening.Render("listening")
	case StateConnecting:
		status = styleConnecting.Render("connecting…")
	case StateError:
		msg := "error"
		if f.err != nil {
			msg = "error: " + f.err.Error()
		}
		status = styleError.Render(msg)
	default:
		if f.spec.Enabled {
			status = styleConnecting.Render("stopped")
		} else {
			status = styleDisabled.Render("disabled")
		}
	}
	return fmt.Sprintf("%-38s  %s", f.spec.String(), status)
}

func (f forwardItem) Description() string { return "" }
func (f forwardItem) FilterValue() string { return f.spec.String() }

// ── view mode ─────────────────────────────────────────────────────────────────

type viewMode int

const (
	modeList viewMode = iota
	modeAdd
)

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	gateway string
	manager *Manager
	config  Config

	statuses []TunnelState
	errors   []error

	// activeConns[specIndex] = list of client addresses currently connected
	activeConns map[int][]string

	list      list.Model
	textInput textinput.Model
	mode      viewMode
	inputErr  string

	width, height int
}

func NewModel(gateway string, mgr *Manager, cfg Config) Model {
	// List with no filtering, no status bar, no help.
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(lipgloss.Color("15")).
		BorderForeground(lipgloss.Color("6"))
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(lipgloss.Color("252"))

	l := list.New(nil, delegate, 0, 0)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowTitle(false)

	ti := textinput.New()
	ti.Placeholder = "port  or  port:host:port  or  bind:port:host:port"
	ti.CharLimit = 80

	n := len(cfg.Forwardings)
	m := Model{
		gateway:     gateway,
		manager:     mgr,
		config:      cfg,
		statuses:    make([]TunnelState, n),
		errors:      make([]error, n),
		activeConns: make(map[int][]string),
		list:        l,
		textInput:   ti,
		mode:        modeList,
	}
	m.rebuildListItems()
	return m
}

// ── tea.Model interface ───────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	// Start all enabled tunnels.
	for i, spec := range m.config.Forwardings {
		if spec.Enabled {
			m.manager.StartTunnel(i, spec)
		}
	}
	return tea.Batch(
		waitForStatus(m.manager.StatusCh()),
		waitForConn(m.manager.ConnCh()),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, m.topPaneHeight())
		return m, nil

	case TunnelStatus:
		if msg.SpecIndex >= 0 && msg.SpecIndex < len(m.statuses) {
			m.statuses[msg.SpecIndex] = msg.State
			m.errors[msg.SpecIndex] = msg.Err
			m.rebuildListItems()
		}
		return m, waitForStatus(m.manager.StatusCh())

	case ConnEvent:
		if msg.Open {
			m.activeConns[msg.SpecIndex] = append(m.activeConns[msg.SpecIndex], msg.RemoteAddr)
		} else {
			addrs := m.activeConns[msg.SpecIndex]
			for i, a := range addrs {
				if a == msg.RemoteAddr {
					m.activeConns[msg.SpecIndex] = append(addrs[:i], addrs[i+1:]...)
					break
				}
			}
		}
		return m, waitForConn(m.manager.ConnCh())

	case tea.KeyMsg:
		if m.mode == modeAdd {
			return m.handleAddInput(msg)
		}
		return m.handleListKeys(msg)
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := styleHeader.Render("portforward: " + m.gateway)

	divider := styleDivider.Render(strings.Repeat("─", m.width))

	var topBody string
	if m.mode == modeAdd {
		prompt := styleInputPrompt.Render("Add forwarding: ")
		topBody = prompt + m.textInput.View()
		if m.inputErr != "" {
			topBody += "\n" + styleInputError.Render("  "+m.inputErr)
		}
	} else {
		topBody = m.list.View()
	}

	bottomTitle := stylePaneTitle.Render("Active connections")
	bottomBody := m.renderConnections()

	help := styleDivider.Render(strings.Repeat("─", m.width)) + "\n"
	if m.mode == modeAdd {
		help += styleDivider.Render("  enter: confirm   esc: cancel")
	} else {
		help += styleDivider.Render("  a: add   d: delete   enter/space: toggle   q: quit")
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		divider,
		topBody,
		divider,
		bottomTitle,
		bottomBody,
		help,
	)
}

// ── key handlers ──────────────────────────────────────────────────────────────

func (m Model) handleListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.manager.Shutdown()
		_ = SaveConfig(m.gateway, m.config)
		return m, tea.Quit

	case "a":
		m.mode = modeAdd
		m.textInput.Focus()
		m.textInput.Reset()
		m.inputErr = ""
		return m, textinput.Blink

	case "d", "delete":
		idx := m.selectedSpecIndex()
		if idx < 0 || idx >= len(m.config.Forwardings) {
			return m, nil
		}
		// Stop all, remove entry, re-index and restart remaining enabled tunnels.
		m.manager.StopAll()
		m.config.Forwardings = append(m.config.Forwardings[:idx], m.config.Forwardings[idx+1:]...)
		m.statuses = append(m.statuses[:idx], m.statuses[idx+1:]...)
		m.errors = append(m.errors[:idx], m.errors[idx+1:]...)
		// Rebuild activeConns map with shifted indices.
		newConns := make(map[int][]string)
		for k, v := range m.activeConns {
			if k < idx {
				newConns[k] = v
			} else if k > idx {
				newConns[k-1] = v
			}
		}
		m.activeConns = newConns
		// Restart all enabled with corrected indices.
		for i, spec := range m.config.Forwardings {
			if spec.Enabled {
				m.manager.StartTunnel(i, spec)
			}
		}
		m.rebuildListItems()
		_ = SaveConfig(m.gateway, m.config)
		return m, nil

	case "enter", " ":
		idx := m.selectedSpecIndex()
		if idx < 0 || idx >= len(m.config.Forwardings) {
			return m, nil
		}
		spec := &m.config.Forwardings[idx]
		spec.Enabled = !spec.Enabled
		if spec.Enabled {
			m.manager.StartTunnel(idx, *spec)
		} else {
			m.manager.StopTunnel(idx)
			m.statuses[idx] = StateStopped
			m.errors[idx] = nil
		}
		m.rebuildListItems()
		_ = SaveConfig(m.gateway, m.config)
		return m, nil
	}

	// Pass remaining keys through to the list (j/k, arrow keys, etc.)
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) handleAddInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		spec, err := ParseSpec(m.textInput.Value())
		if err != nil {
			m.inputErr = err.Error()
			return m, nil
		}
		idx := len(m.config.Forwardings)
		m.config.Forwardings = append(m.config.Forwardings, spec)
		m.statuses = append(m.statuses, StateStopped)
		m.errors = append(m.errors, nil)
		m.manager.StartTunnel(idx, spec)
		m.rebuildListItems()
		_ = SaveConfig(m.gateway, m.config)
		m.mode = modeList
		return m, nil

	case "esc":
		m.mode = modeList
		m.inputErr = ""
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (m *Model) rebuildListItems() {
	sortedItems := make([]forwardItem, len(m.config.Forwardings))
	for i, spec := range m.config.Forwardings {
		sortedItems[i] = forwardItem{
			idx:   i,
			spec:  spec,
			state: m.statuses[i],
			err:   m.errors[i],
		}
	}
	sort.SliceStable(sortedItems, func(i, j int) bool {
		return sortedItems[i].spec.LocalPort < sortedItems[j].spec.LocalPort
	})
	items := make([]list.Item, len(sortedItems))
	for i, item := range sortedItems {
		items[i] = item
	}
	m.list.SetItems(items)
}

func (m Model) selectedSpecIndex() int {
	item, ok := m.list.SelectedItem().(forwardItem)
	if !ok {
		return -1
	}
	return item.idx
}

func (m *Model) topPaneHeight() int {
	// header(1) + divider(1) + divider(1) + bottomTitle(1) + bottomBody + help(2)
	// Allocate 60% of remaining space to the top pane, min 3.
	available := m.height - 6 // rough fixed chrome lines
	if available < 3 {
		available = 3
	}
	top := available * 6 / 10
	if top < 3 {
		top = 3
	}
	return top
}

func (m *Model) bottomPaneHeight() int {
	available := m.height - 6
	if available < 1 {
		return 1
	}
	top := available * 6 / 10
	bottom := available - top
	if bottom < 1 {
		bottom = 1
	}
	return bottom
}

func (m *Model) renderConnections() string {
	maxLines := m.bottomPaneHeight()
	type connLine struct {
		localPort int
		addr      string
	}
	var lines []connLine
	for idx, addrs := range m.activeConns {
		if idx >= len(m.config.Forwardings) {
			continue
		}
		port := m.config.Forwardings[idx].LocalPort
		for _, a := range addrs {
			lines = append(lines, connLine{port, a})
		}
	}
	if len(lines) == 0 {
		return styleDisabled.Render("  (none)")
	}
	// Show only the last maxLines entries.
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(fmt.Sprintf("  %-6d  %s\n",
			l.localPort,
			styleConnAddr.Render(l.addr),
		))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ── channel-to-Cmd bridges ────────────────────────────────────────────────────

func waitForStatus(ch <-chan TunnelStatus) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func waitForConn(ch <-chan ConnEvent) tea.Cmd {
	return func() tea.Msg { return <-ch }
}
