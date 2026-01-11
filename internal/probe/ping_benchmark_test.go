package probe

import (
	"context"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestPingLatencyComparison performs a comparative test between native ICMP probe
// and the system ping command to measure the overhead difference.
// Run with: go test -v -run TestPingLatencyComparison ./internal/probe/
func TestPingLatencyComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ping benchmark in short mode")
	}

	const iterations = 100
	const target = "127.0.0.1"
	timeout := 5 * time.Second

	// Force detect ICMP capability
	detectICMPCapability()
	if icmpNetwork == "" {
		t.Skip("native ICMP not available, cannot benchmark")
	}

	t.Logf("Using ICMP network: %s, kernel timestamps: %v", icmpNetwork, useKernelTimestamp)
	t.Logf("Running %d iterations against %s", iterations, target)

	nativeLatencies := make([]float64, 0, iterations)
	commandLatencies := make([]float64, 0, iterations)
	nativeErrors := 0
	commandErrors := 0

	// Pattern for parsing ping command output
	pattern := regexp.MustCompile(`time=(?P<val>[0-9.]+) ms`)

	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		// Run native ICMP
		nativeStart := time.Now()
		nativeLat, err := runNativeICMP(ctx, target, icmpNetwork)
		nativeDuration := time.Since(nativeStart)
		if err != nil {
			nativeErrors++
		} else {
			// nativeLat is the measured RTT, nativeDuration includes all overhead
			_ = nativeDuration
			nativeLatencies = append(nativeLatencies, nativeLat)
		}

		// Run ping command
		cmdStart := time.Now()
		cmd := exec.CommandContext(ctx, "ping", "-c", "1", target)
		output, err := cmd.CombinedOutput()
		cmdDuration := time.Since(cmdStart)
		_ = cmdDuration

		if err != nil {
			commandErrors++
		} else {
			matches := pattern.FindStringSubmatch(string(output))
			if matches != nil {
				valIdx := pattern.SubexpIndex("val")
				if valIdx >= 0 && valIdx < len(matches) {
					val, err := strconv.ParseFloat(matches[valIdx], 64)
					if err == nil {
						// Convert ms to ns
						commandLatencies = append(commandLatencies, val*1e6)
					}
				}
			}
		}

		cancel()

		// Small delay between iterations to avoid overwhelming
		time.Sleep(10 * time.Millisecond)
	}

	// Calculate statistics
	t.Logf("\n=== RESULTS ===")
	t.Logf("Native ICMP: %d successful, %d errors", len(nativeLatencies), nativeErrors)
	t.Logf("Ping Command: %d successful, %d errors", len(commandLatencies), commandErrors)

	if len(nativeLatencies) > 0 {
		nativeStats := calcStats(nativeLatencies)
		t.Logf("\nNative ICMP Latency (reported RTT):")
		t.Logf("  Min:    %10.3f µs", nativeStats.min/1e3)
		t.Logf("  Max:    %10.3f µs", nativeStats.max/1e3)
		t.Logf("  Mean:   %10.3f µs", nativeStats.mean/1e3)
		t.Logf("  Median: %10.3f µs", nativeStats.median/1e3)
		t.Logf("  P95:    %10.3f µs", nativeStats.p95/1e3)
		t.Logf("  P99:    %10.3f µs", nativeStats.p99/1e3)
		t.Logf("  StdDev: %10.3f µs", nativeStats.stddev/1e3)
	}

	if len(commandLatencies) > 0 {
		cmdStats := calcStats(commandLatencies)
		t.Logf("\nPing Command Latency (reported time=):")
		t.Logf("  Min:    %10.3f µs", cmdStats.min/1e3)
		t.Logf("  Max:    %10.3f µs", cmdStats.max/1e3)
		t.Logf("  Mean:   %10.3f µs", cmdStats.mean/1e3)
		t.Logf("  Median: %10.3f µs", cmdStats.median/1e3)
		t.Logf("  P95:    %10.3f µs", cmdStats.p95/1e3)
		t.Logf("  P99:    %10.3f µs", cmdStats.p99/1e3)
		t.Logf("  StdDev: %10.3f µs", cmdStats.stddev/1e3)
	}

	if len(nativeLatencies) > 0 && len(commandLatencies) > 0 {
		nativeMean := calcStats(nativeLatencies).mean
		cmdMean := calcStats(commandLatencies).mean
		diff := nativeMean - cmdMean
		t.Logf("\n=== COMPARISON ===")
		t.Logf("Mean difference (native - command): %.3f µs", diff/1e3)
		if diff > 0 {
			t.Logf("Native probe is %.3f µs SLOWER than ping command", diff/1e3)
		} else {
			t.Logf("Native probe is %.3f µs FASTER than ping command", -diff/1e3)
		}
	}
}

// BenchmarkNativeICMP benchmarks just the native ICMP probe
func BenchmarkNativeICMP(b *testing.B) {
	const target = "127.0.0.1"
	timeout := 5 * time.Second

	detectICMPCapability()
	if icmpNetwork == "" {
		b.Skip("native ICMP not available")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err := runNativeICMP(ctx, target, icmpNetwork)
		cancel()
		if err != nil {
			b.Logf("error: %v", err)
		}
	}
}

// BenchmarkPingCommand benchmarks the ping command fallback
func BenchmarkPingCommand(b *testing.B) {
	const target = "127.0.0.1"
	timeout := 5 * time.Second

	cfg := Config{
		Type:       "ping",
		Address:    target,
		Command:    "ping",
		Args:       []string{"-c", "1", target},
		Pattern:    `time=(?P<val>[0-9.]+) ms`,
		Multiplier: 1e6,
		Timeout:    timeout,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err := runCommand(ctx, cfg)
		cancel()
		if err != nil {
			b.Logf("error: %v", err)
		}
	}
}

type stats struct {
	min, max, mean, median, p95, p99, stddev float64
}

func calcStats(data []float64) stats {
	if len(data) == 0 {
		return stats{}
	}

	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))

	var sqDiffSum float64
	for _, v := range sorted {
		diff := v - mean
		sqDiffSum += diff * diff
	}
	stddev := 0.0
	if len(sorted) > 1 {
		variance := sqDiffSum / float64(len(sorted)-1)
		stddev = math.Sqrt(variance)
	}

	return stats{
		min:    sorted[0],
		max:    sorted[len(sorted)-1],
		mean:   mean,
		median: sorted[len(sorted)/2],
		p95:    sorted[int(float64(len(sorted))*0.95)],
		p99:    sorted[int(float64(len(sorted))*0.99)],
		stddev: stddev,
	}
}
