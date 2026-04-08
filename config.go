package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// ForwardSpec describes a single local port forwarding rule.
type ForwardSpec struct {
	BindAddr   string `json:"bind_addr"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	Enabled    bool   `json:"enabled"`
}

func (f ForwardSpec) LocalAddr() string {
	return fmt.Sprintf("%s:%d", f.BindAddr, f.LocalPort)
}

func (f ForwardSpec) RemoteAddr() string {
	return fmt.Sprintf("%s:%d", f.RemoteHost, f.RemotePort)
}

func (f ForwardSpec) String() string {
	return fmt.Sprintf("%s:%d -> %s:%d", f.BindAddr, f.LocalPort, f.RemoteHost, f.RemotePort)
}

// ParseSpec parses a forwarding spec string in one of three forms:
//
//	port                         → 127.0.0.1:port -> localhost:port
//	port:remotehost:remoteport   → 127.0.0.1:port -> remotehost:remoteport
//	bindaddr:port:remotehost:remoteport
func ParseSpec(s string) (ForwardSpec, error) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			return ForwardSpec{}, fmt.Errorf("invalid port %q", parts[0])
		}
		return ForwardSpec{
			BindAddr:   "127.0.0.1",
			LocalPort:  port,
			RemoteHost: "localhost",
			RemotePort: port,
			Enabled:    true,
		}, nil

	case 3:
		localPort, err := strconv.Atoi(parts[0])
		if err != nil || localPort < 1 || localPort > 65535 {
			return ForwardSpec{}, fmt.Errorf("invalid local port %q", parts[0])
		}
		remotePort, err := strconv.Atoi(parts[2])
		if err != nil || remotePort < 1 || remotePort > 65535 {
			return ForwardSpec{}, fmt.Errorf("invalid remote port %q", parts[2])
		}
		if parts[1] == "" {
			return ForwardSpec{}, fmt.Errorf("remote host cannot be empty")
		}
		return ForwardSpec{
			BindAddr:   "127.0.0.1",
			LocalPort:  localPort,
			RemoteHost: parts[1],
			RemotePort: remotePort,
			Enabled:    true,
		}, nil

	case 4:
		localPort, err := strconv.Atoi(parts[1])
		if err != nil || localPort < 1 || localPort > 65535 {
			return ForwardSpec{}, fmt.Errorf("invalid local port %q", parts[1])
		}
		remotePort, err := strconv.Atoi(parts[3])
		if err != nil || remotePort < 1 || remotePort > 65535 {
			return ForwardSpec{}, fmt.Errorf("invalid remote port %q", parts[3])
		}
		if parts[0] == "" {
			return ForwardSpec{}, fmt.Errorf("bind address cannot be empty")
		}
		if parts[2] == "" {
			return ForwardSpec{}, fmt.Errorf("remote host cannot be empty")
		}
		return ForwardSpec{
			BindAddr:   parts[0],
			LocalPort:  localPort,
			RemoteHost: parts[2],
			RemotePort: remotePort,
			Enabled:    true,
		}, nil

	default:
		return ForwardSpec{}, fmt.Errorf("invalid spec %q: use port, port:host:port, or bind:port:host:port", s)
	}
}

// Config holds persisted forwardings for a gateway.
type Config struct {
	Forwardings []ForwardSpec `json:"forwardings"`
}

func configPath(gateway string) string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "portforward", gateway+".json")
}

func LoadConfig(gateway string) (Config, error) {
	data, err := os.ReadFile(configPath(gateway))
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	sortForwardings(cfg.Forwardings)
	return cfg, nil
}

func SaveConfig(gateway string, cfg Config) error {
	p := configPath(gateway)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	sorted := Config{
		Forwardings: append([]ForwardSpec(nil), cfg.Forwardings...),
	}
	sortForwardings(sorted.Forwardings)
	data, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func sortForwardings(forwardings []ForwardSpec) {
	sort.SliceStable(forwardings, func(i, j int) bool {
		return forwardings[i].LocalPort < forwardings[j].LocalPort
	})
}

// GatewayInfo holds resolved SSH connection details.
type GatewayInfo struct {
	Alias string
	Host  string
	User  string
	Port  string
	Jumps []SSHHost
}

type SSHHost struct {
	Alias string
	Host  string
	User  string
	Port  string
}

type jumpSpec struct {
	ref  string
	user string
	port string
}

// ResolveGateway resolves an SSH alias via ~/.ssh/config.
func ResolveGateway(alias, proxyJumpOverride string) (GatewayInfo, error) {
	target, err := resolveSSHHost(alias, "", "")
	if err != nil {
		return GatewayInfo{}, err
	}

	jumpSpec := proxyJumpOverride
	if jumpSpec == "" {
		jumpSpec, err = ssh_config.GetStrict(alias, "ProxyJump")
		if err != nil {
			return GatewayInfo{}, err
		}
	}

	jumps, err := resolveProxyJumpSpec(jumpSpec, map[string]bool{})
	if err != nil {
		return GatewayInfo{}, err
	}

	return GatewayInfo{
		Alias: target.Alias,
		Host:  target.Host,
		User:  target.User,
		Port:  target.Port,
		Jumps: jumps,
	}, nil
}

func resolveSSHHost(ref, userOverride, portOverride string) (SSHHost, error) {
	host, err := ssh_config.GetStrict(ref, "Hostname")
	if err != nil {
		return SSHHost{}, err
	}
	if host == "" {
		host = ref
	}

	user, err := ssh_config.GetStrict(ref, "User")
	if err != nil {
		return SSHHost{}, err
	}
	if user == "" {
		user = defaultSSHUser()
	}
	if userOverride != "" {
		user = userOverride
	}
	if user == "" {
		return SSHHost{}, fmt.Errorf("user not configured for %q", ref)
	}

	port, err := ssh_config.GetStrict(ref, "Port")
	if err != nil {
		return SSHHost{}, err
	}
	if port == "" {
		port = "22"
	}
	if portOverride != "" {
		port = portOverride
	}
	if err := validatePort(port); err != nil {
		return SSHHost{}, fmt.Errorf("invalid port for %q: %w", ref, err)
	}

	return SSHHost{
		Alias: ref,
		Host:  host,
		User:  user,
		Port:  port,
	}, nil
}

func resolveProxyJumpSpec(spec string, seen map[string]bool) ([]SSHHost, error) {
	jumps, err := parseProxyJumpSpec(spec)
	if err != nil || len(jumps) == 0 {
		return nil, err
	}

	resolved := make([]SSHHost, 0, len(jumps))
	for _, jump := range jumps {
		nested, err := resolveConfiguredProxyJumps(jump.ref, seen)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, nested...)

		host, err := resolveSSHHost(jump.ref, jump.user, jump.port)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, host)
	}
	return resolved, nil
}

func resolveConfiguredProxyJumps(alias string, seen map[string]bool) ([]SSHHost, error) {
	key := strings.ToLower(alias)
	if seen[key] {
		return nil, fmt.Errorf("ProxyJump cycle detected at %q", alias)
	}

	seen[key] = true
	defer delete(seen, key)

	spec, err := ssh_config.GetStrict(alias, "ProxyJump")
	if err != nil {
		return nil, err
	}
	return resolveProxyJumpSpec(spec, seen)
}

func parseProxyJumpSpec(spec string) ([]jumpSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "none") {
		return nil, nil
	}

	parts := strings.Split(spec, ",")
	jumps := make([]jumpSpec, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid ProxyJump %q", spec)
		}
		if strings.EqualFold(part, "none") {
			return nil, fmt.Errorf("invalid ProxyJump %q", spec)
		}

		jump, err := parseJumpEndpoint(part)
		if err != nil {
			return nil, err
		}
		jumps = append(jumps, jump)
	}
	return jumps, nil
}

func parseJumpEndpoint(s string) (jumpSpec, error) {
	if strings.HasPrefix(s, "ssh://") {
		return parseJumpURI(s)
	}

	var jump jumpSpec
	hostSpec := s
	if at := strings.LastIndex(s, "@"); at >= 0 {
		jump.user = s[:at]
		hostSpec = s[at+1:]
		if jump.user == "" {
			return jumpSpec{}, fmt.Errorf("invalid jump host %q", s)
		}
	}

	host, port, err := splitJumpHostPort(hostSpec)
	if err != nil {
		return jumpSpec{}, err
	}
	if host == "" {
		return jumpSpec{}, fmt.Errorf("invalid jump host %q", s)
	}

	jump.ref = host
	jump.port = port
	return jump, nil
}

func parseJumpURI(s string) (jumpSpec, error) {
	u, err := url.Parse(s)
	if err != nil {
		return jumpSpec{}, fmt.Errorf("invalid jump host %q: %w", s, err)
	}
	if u.Scheme != "ssh" {
		return jumpSpec{}, fmt.Errorf("invalid jump host %q", s)
	}
	if u.Hostname() == "" {
		return jumpSpec{}, fmt.Errorf("invalid jump host %q", s)
	}
	if u.Path != "" && u.Path != "/" {
		return jumpSpec{}, fmt.Errorf("invalid jump host %q", s)
	}

	return jumpSpec{
		ref:  u.Hostname(),
		user: u.User.Username(),
		port: u.Port(),
	}, nil
}

func splitJumpHostPort(s string) (string, string, error) {
	if s == "" {
		return "", "", fmt.Errorf("invalid jump host %q", s)
	}

	if strings.HasPrefix(s, "[") {
		if strings.Contains(s, "]:") {
			host, port, err := net.SplitHostPort(s)
			if err != nil {
				return "", "", fmt.Errorf("invalid jump host %q", s)
			}
			return host, port, nil
		}
		if strings.HasSuffix(s, "]") {
			return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), "", nil
		}
		return "", "", fmt.Errorf("invalid jump host %q", s)
	}

	colons := strings.Count(s, ":")
	switch {
	case colons == 0:
		return s, "", nil
	case colons == 1:
		idx := strings.LastIndex(s, ":")
		host := s[:idx]
		port := s[idx+1:]
		if host == "" || port == "" {
			return "", "", fmt.Errorf("invalid jump host %q", s)
		}
		if err := validatePort(port); err != nil {
			return "", "", fmt.Errorf("invalid jump host %q", s)
		}
		return host, port, nil
	default:
		return "", "", fmt.Errorf("invalid jump host %q: bracket IPv6 addresses", s)
	}
}

func defaultSSHUser() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return os.Getenv("LOGNAME")
}

func validatePort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q must be between 1 and 65535", port)
	}
	return nil
}
