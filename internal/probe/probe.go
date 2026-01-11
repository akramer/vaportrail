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
		res, err = runPing(ctx, cfg)
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
	// Query the DNS server at `address` for "example.com" A record
	// using raw DNS packet construction

	targetAddr := address
	if !strings.Contains(targetAddr, ":") {
		targetAddr = targetAddr + ":53"
	}

	// Build a minimal DNS query packet
	// Header: 12 bytes
	// Question: variable (domain name + type + class)

	// Transaction ID (2 bytes) - random
	txID := uint16(rand.Intn(65536))

	// Flags (2 bytes): standard query, recursion desired
	// 0x0100 = recursion desired
	flags := uint16(0x0100)

	// Counts (each 2 bytes)
	qdCount := uint16(1) // 1 question
	anCount := uint16(0)
	nsCount := uint16(0)
	arCount := uint16(0)

	// Build header (12 bytes)
	header := make([]byte, 12)
	header[0] = byte(txID >> 8)
	header[1] = byte(txID)
	header[2] = byte(flags >> 8)
	header[3] = byte(flags)
	header[4] = byte(qdCount >> 8)
	header[5] = byte(qdCount)
	header[6] = byte(anCount >> 8)
	header[7] = byte(anCount)
	header[8] = byte(nsCount >> 8)
	header[9] = byte(nsCount)
	header[10] = byte(arCount >> 8)
	header[11] = byte(arCount)

	// Build question section for "example.com"
	// Domain name encoding: length-prefixed labels, ending with 0
	// example.com -> 7example3com0
	domain := []byte{
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		3, 'c', 'o', 'm',
		0, // null terminator
	}

	// QTYPE: A record = 1
	// QCLASS: IN = 1
	question := append(domain, 0, 1, 0, 1) // type A (0x0001), class IN (0x0001)

	// Complete packet
	packet := append(header, question...)

	// Create UDP connection
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "udp", targetAddr)
	if err != nil {
		return 0, fmt.Errorf("failed to dial DNS server: %w", err)
	}
	defer conn.Close()

	// Set deadline from context
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	start := time.Now()

	// Send query
	_, err = conn.Write(packet)
	if err != nil {
		return 0, fmt.Errorf("failed to send DNS query: %w", err)
	}

	// Read response (512 bytes is standard max for UDP DNS)
	response := make([]byte, 512)
	n, err := conn.Read(response)
	if err != nil {
		return 0, fmt.Errorf("failed to read DNS response: %w", err)
	}

	elapsed := float64(time.Since(start).Nanoseconds())

	// Basic validation: check we got at least a header and the transaction ID matches
	if n < 12 {
		return 0, fmt.Errorf("DNS response too short: %d bytes", n)
	}
	respTxID := uint16(response[0])<<8 | uint16(response[1])
	if respTxID != txID {
		return 0, fmt.Errorf("DNS response transaction ID mismatch: got %d, expected %d", respTxID, txID)
	}

	// Check RCODE in flags (lower 4 bits of byte 3)
	rcode := response[3] & 0x0F
	if rcode != 0 {
		return 0, fmt.Errorf("DNS query failed with RCODE: %d", rcode)
	}

	return elapsed, nil
}

// runPing executes the ping command and parses the result
func runPing(ctx context.Context, cfg Config) (float64, error) {
	return runCommand(ctx, cfg)
}

func runCommand(ctx context.Context, cfg Config) (float64, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("probe timed out after %v", cfg.Timeout)
		}
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
