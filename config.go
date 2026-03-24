package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	return cfg, nil
}

func SaveConfig(gateway string, cfg Config) error {
	p := configPath(gateway)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// GatewayInfo holds resolved SSH connection details.
type GatewayInfo struct {
	Alias string
	Host  string
	User  string
	Port  string
}

// ResolveGateway resolves an SSH alias via ~/.ssh/config.
func ResolveGateway(alias string) (GatewayInfo, error) {
	host, _ := ssh_config.GetStrict(alias, "Hostname")
	if host == "" {
		host = alias
	}
	user, _ := ssh_config.GetStrict(alias, "User")
	if user == "" {
		user = os.Getenv("USER")
		if user == "" {
			user = os.Getenv("LOGNAME")
		}
	}
	port, _ := ssh_config.GetStrict(alias, "Port")
	if port == "" {
		port = "22"
	}
	return GatewayInfo{
		Alias: alias,
		Host:  host,
		User:  user,
		Port:  port,
	}, nil
}
