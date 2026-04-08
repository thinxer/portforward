package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type TunnelState int

const (
	StateStopped    TunnelState = iota
	StateConnecting             // trying to establish SSH connection
	StateListening              // net.Listen active, accepting connections
	StateError                  // terminal error (e.g. port in use)
)

// TunnelStatus is sent on statusCh when a forwarding's state changes.
type TunnelStatus struct {
	SpecIndex int
	State     TunnelState
	Err       error
}

// ConnEvent is sent on connCh when an individual proxied connection opens or closes.
type ConnEvent struct {
	SpecIndex  int
	RemoteAddr string // local client address (e.g. 127.0.0.1:54321)
	Open       bool
}

// Manager owns the SSH client and all tunnel goroutines.
type Manager struct {
	gateway GatewayInfo
	sshCfg  *ssh.ClientConfig

	mu           sync.Mutex
	client       *ssh.Client // nil when disconnected
	clientCloser []io.Closer
	cancels      map[int]context.CancelFunc
	enabled      map[int]ForwardSpec // specs currently running (for restart on reconnect)

	statusCh chan TunnelStatus
	connCh   chan ConnEvent

	rootCtx    context.Context
	rootCancel context.CancelFunc
}

// NewManager creates a Manager wired to the SSH agent. It does not dial yet.
func NewManager(gw GatewayInfo) (*Manager, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set — is ssh-agent running?")
	}
	agentConn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	agentClient := agent.NewClient(agentConn)

	sshCfg := &ssh.ClientConfig{
		User:            gw.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient.Signers)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		gateway:    gw,
		sshCfg:     sshCfg,
		cancels:    make(map[int]context.CancelFunc),
		enabled:    make(map[int]ForwardSpec),
		statusCh:   make(chan TunnelStatus, 64),
		connCh:     make(chan ConnEvent, 128),
		rootCtx:    ctx,
		rootCancel: cancel,
	}
	go m.keepaliveLoop()
	return m, nil
}

func (m *Manager) StatusCh() <-chan TunnelStatus { return m.statusCh }
func (m *Manager) ConnCh() <-chan ConnEvent      { return m.connCh }

// ensureConnected returns the current ssh.Client, dialing if needed.
// Caller must NOT hold m.mu.
func (m *Manager) ensureConnected(ctx context.Context) (*ssh.Client, error) {
	m.mu.Lock()
	if m.client != nil {
		client := m.client
		m.mu.Unlock()
		return client, nil
	}
	m.mu.Unlock()

	c, closers, err := m.dialGateway(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		closeAll(closers)
		return m.client, nil
	}
	m.client = c
	m.clientCloser = closers
	return c, nil
}

// keepaliveLoop sends periodic keepalives and resets the connection on failure.
func (m *Manager) keepaliveLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.rootCtx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			c := m.client
			m.mu.Unlock()
			if c == nil {
				continue
			}
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				m.mu.Lock()
				if m.client == c { // still the same client
					m.closeClientLocked()
				}
				m.mu.Unlock()
				m.restartAllEnabled()
			}
		}
	}
}

// StartTunnel starts a tunnel goroutine for the given spec at index idx.
func (m *Manager) StartTunnel(idx int, spec ForwardSpec) {
	m.mu.Lock()
	if cancel, ok := m.cancels[idx]; ok {
		cancel()
		delete(m.cancels, idx)
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.cancels[idx] = cancel
	m.enabled[idx] = spec
	m.mu.Unlock()

	go m.runTunnel(ctx, idx, spec)
}

// StopTunnel cancels the tunnel goroutine at idx.
func (m *Manager) StopTunnel(idx int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[idx]; ok {
		cancel()
		delete(m.cancels, idx)
	}
	delete(m.enabled, idx)
}

// StopAll cancels every tunnel goroutine.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.cancels {
		cancel()
	}
	m.cancels = make(map[int]context.CancelFunc)
	m.enabled = make(map[int]ForwardSpec)
}

// restartAllEnabled stops all tunnels and relaunches them — called after reconnect.
func (m *Manager) restartAllEnabled() {
	m.mu.Lock()
	snapshot := make(map[int]ForwardSpec, len(m.enabled))
	for k, v := range m.enabled {
		snapshot[k] = v
	}
	for _, cancel := range m.cancels {
		cancel()
	}
	m.cancels = make(map[int]context.CancelFunc)
	m.mu.Unlock()

	for idx, spec := range snapshot {
		m.StartTunnel(idx, spec)
	}
}

// Shutdown stops all tunnels and closes the SSH connection.
func (m *Manager) Shutdown() {
	m.rootCancel()
	m.mu.Lock()
	m.closeClientLocked()
	m.mu.Unlock()
}

// runTunnel is the main goroutine for a single forwarding.
func (m *Manager) runTunnel(ctx context.Context, idx int, spec ForwardSpec) {
	defer func() {
		select {
		case m.statusCh <- TunnelStatus{idx, StateStopped, nil}:
		default:
		}
	}()

	// Connect with exponential backoff.
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.send(TunnelStatus{idx, StateConnecting, nil})
		_, err := m.ensureConnected(ctx)
		if err == nil {
			break
		}
		m.send(TunnelStatus{idx, StateError, err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}

	// Bind local listener.
	ln, err := net.Listen("tcp", spec.LocalAddr())
	if err != nil {
		m.send(TunnelStatus{idx, StateError, err})
		return
	}
	defer ln.Close()

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	m.send(TunnelStatus{idx, StateListening, nil})

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				m.send(TunnelStatus{idx, StateError, err})
				return
			}
		}
		go m.handleConn(ctx, conn, spec, idx)
	}
}

func (m *Manager) handleConn(ctx context.Context, local net.Conn, spec ForwardSpec, idx int) {
	defer local.Close()
	clientAddr := local.RemoteAddr().String()

	select {
	case m.connCh <- ConnEvent{idx, clientAddr, true}:
	default:
	}
	defer func() {
		select {
		case m.connCh <- ConnEvent{idx, clientAddr, false}:
		default:
		}
	}()

	client, err := m.ensureConnected(ctx)
	if err != nil {
		return
	}

	remote, err := client.Dial("tcp", spec.RemoteAddr())
	if err != nil {
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (m *Manager) send(s TunnelStatus) {
	select {
	case m.statusCh <- s:
	default:
	}
}

func (m *Manager) dialGateway(_ context.Context) (*ssh.Client, []io.Closer, error) {
	path := append([]SSHHost(nil), m.gateway.Jumps...)
	path = append(path, SSHHost{
		Alias: m.gateway.Alias,
		Host:  m.gateway.Host,
		User:  m.gateway.User,
		Port:  m.gateway.Port,
	})

	var (
		client  *ssh.Client
		closers []io.Closer
	)

	for i, host := range path {
		addr := net.JoinHostPort(host.Host, host.Port)
		cfg := m.sshConfig(host.User)

		if i == 0 {
			next, err := ssh.Dial("tcp", addr, cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("connect to %s: %w", host.Alias, err)
			}
			client = next
			closers = append(closers, next)
			continue
		}

		conn, err := client.Dial("tcp", addr)
		if err != nil {
			closeAll(closers)
			return nil, nil, fmt.Errorf("connect to %s via jump host: %w", host.Alias, err)
		}

		cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
		if err != nil {
			_ = conn.Close()
			closeAll(closers)
			return nil, nil, fmt.Errorf("ssh handshake with %s: %w", host.Alias, err)
		}

		next := ssh.NewClient(cc, chans, reqs)
		client = next
		closers = append(closers, next)
	}

	return client, closers, nil
}

func (m *Manager) sshConfig(user string) *ssh.ClientConfig {
	cfg := *m.sshCfg
	cfg.User = user
	return &cfg
}

func (m *Manager) closeClientLocked() {
	m.client = nil
	closers := m.clientCloser
	m.clientCloser = nil
	closeAll(closers)
}

func closeAll(closers []io.Closer) {
	for i := len(closers) - 1; i >= 0; i-- {
		_ = closers[i].Close()
	}
}
