package probe

import (
	"fmt"
	"math/rand"
	"os/exec"
	"regexp"
	"strconv"
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

// Config defines how to run a probe and parse its output.
type Config struct {
	Command string   `json:"command"` // Command to execute, e.g. "ping", "curl"
	Args    []string `json:"args"`    // Arguments, e.g. ["-c", "1", "google.com"]
	// Regex pattern to extract a metric. Must contain a named group "val".
	// The value should be a float number.
	// Example for ping: "time=(?P<val>[0-9.]+) ms"
	Pattern string `json:"pattern"`
	// Multiplier to convert the extracted value to Nanoseconds.
	// e.g. if extraction is in ms, Multiplier should be 1,000,000.
	Multiplier float64 `json:"multiplier"`
}

// GetConfig returns the probe configuration for a given type and target address.
func GetConfig(probeType, address string) (Config, error) {
	switch probeType {
	case "ping":
		return Config{
			Command:    "ping",
			Args:       []string{"-c", "1", address},
			Pattern:    "time=(?P<val>[0-9.]+) ms",
			Multiplier: 1000000,
		}, nil
	case "http":
		return Config{
			Command: "curl",
			Args: []string{
				"-w", "time_total: %{time_total}\n",
				"-o", "/dev/null",
				"-s",
				address,
			},
			Pattern:    "time_total: (?P<val>[0-9.]+)",
			Multiplier: 1000000000,
		}, nil
	case "dns":
		return Config{
			Command:    "dig",
			Args:       []string{address},
			Pattern:    "Query time: (?P<val>[0-9]+) msec",
			Multiplier: 1000000,
		}, nil
	default:
		return Config{}, fmt.Errorf("unknown probe type: %s", probeType)
	}
}

// Run executes the probe and returns the latency in nanoseconds.
func Run(cfg Config) (float64, error) {
	// Jitter: Sleep for a random duration between 0 and 100ms to avoid thundering herd on local resources
	time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)

	cmd := exec.Command(cfg.Command, cfg.Args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
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
