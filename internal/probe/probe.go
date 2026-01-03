package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner defines the interface for running a probe.
type Runner interface {
	Run(cfg Config) (float64, error)
}

// RealRunner implements Runner using the actual system commands.
type RealRunner struct{}

func (r RealRunner) Run(cfg Config) (float64, error) {
	return Run(cfg)
}

// Config defines how to run a probe.
type Config struct {
	Type    string `json:"type"`    // "ping", "http", "dns"
	Address string `json:"address"` // Target address

	// Deprecated fields, kept for "ping" command execution
	Command    string        `json:"command"`
	Args       []string      `json:"args"`
	Pattern    string        `json:"pattern"`
	Multiplier float64       `json:"multiplier"`
	Timeout    time.Duration `json:"-"`
}

// GetConfig returns the probe configuration for a given type and target address.
func GetConfig(probeType, address string) (Config, error) {
	cfg := Config{
		Type:    probeType,
		Address: address,
	}

	switch probeType {
	case "ping":
		cfg.Command = "ping"
		cfg.Args = []string{"-c", "1", address}
		cfg.Pattern = "time=(?P<val>[0-9.]+) ms"
		cfg.Multiplier = 1000000
	case "http", "dns":
		// Native implementations don't need Command/Args/Pattern
	default:
		return Config{}, fmt.Errorf("unknown probe type: %s", probeType)
	}
	return cfg, nil
}

// Run executes the probe and returns the latency in nanoseconds.
func Run(cfg Config) (float64, error) {
	// Jitter: Sleep for a random duration between 0 and 100ms to avoid thundering herd on local resources
	time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	var res float64
	var err error

	switch cfg.Type {
	case "http":
		res, err = runHTTP(ctx, cfg.Address)
	case "dns":
		res, err = runDNS(ctx, cfg.Address)
	case "ping":
		res, err = runCommand(ctx, cfg)
	default:
		return 0, fmt.Errorf("unknown probe type: %s", cfg.Type)
	}

	// If success, enforce timeout check. Sometimes net calls might return success slightly after timeout?
	// Or maybe the precision of float64 ns vs duration?
	// Let's be strict.
	if err == nil {
		if res >= float64(cfg.Timeout.Nanoseconds()) {
			return 0, fmt.Errorf("probe timed out: duration %v exceeded limit %v", time.Duration(res), cfg.Timeout)
		}
	}

	if err != nil {
		if strings.Contains(err.Error(), "probe timed out") {
			return 0, err
		}
		if isTimeout(err) {
			return 0, fmt.Errorf("probe timed out: %w", err)
		}
		return 0, err
	}
	return res, nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func runHTTP(ctx context.Context, address string) (float64, error) {
	if !strings.HasPrefix(address, "http") {
		address = "http://" + address
	}

	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// Read body to ensure we measure full transfer time
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}

	return float64(time.Since(start).Nanoseconds()), nil
}

func runDNS(ctx context.Context, address string) (float64, error) {
	// Current behavior: dig @address example.com
	// We want to query the DNS server at `address` for "example.com"

	targetAddr := address
	if !strings.Contains(targetAddr, ":") {
		targetAddr = targetAddr + ":53"
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", targetAddr)
		},
	}

	start := time.Now()
	// querying for "example.com" A record
	_, err := resolver.LookupIP(ctx, "ip4", "example.com")
	if err != nil {
		return 0, err
	}

	return float64(time.Since(start).Nanoseconds()), nil
}

func runCommand(ctx context.Context, cfg Config) (float64, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("probe timed out after %v", cfg.Timeout)
		}
		// If the command fails, we still try to parse the output because some tools (like ping)
		// might exit with non-zero even if we got some RTT data (though unlikely with count=1).
		// However, for single probe, usually error means failure.
		// For now, let's treat execution error as failure.
		return 0, fmt.Errorf("command failed: %v, output: %s", err, string(output))
	}

	re, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return 0, fmt.Errorf("invalid regex pattern: %w", err)
	}

	matches := re.FindStringSubmatch(string(output))
	if matches == nil {
		return 0, fmt.Errorf("pattern not found in output: %s", string(output))
	}

	valIdx := re.SubexpIndex("val")
	if valIdx < 0 || valIdx >= len(matches) {
		return 0, fmt.Errorf("capture group 'val' not found")
	}

	valStr := matches[valIdx]
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse value '%s': %w", valStr, err)
	}

	// Convert to nanoseconds
	valNS := val * cfg.Multiplier
	return valNS, nil
}
