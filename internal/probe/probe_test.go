package probe

import (
	"os/exec"
	"testing"
	"time"
)

func TestGetConfig(t *testing.T) {
	tests := []struct {
		name      string
		probeType string
		address   string
		wantErr   bool
		check     func(*testing.T, Config)
	}{
		{
			name:      "Valid Ping",
			probeType: "ping",
			address:   "1.1.1.1",
			wantErr:   false,
			check: func(t *testing.T, c Config) {
				if c.Type != "ping" {
					t.Errorf("expected type ping, got %s", c.Type)
				}
				if c.Command != "ping" {
					t.Errorf("expected command ping, got %s", c.Command)
				}
				args := c.Args
				if len(args) < 3 || args[1] != "1" {
					t.Errorf("expected -c 1, got %v", args)
				}
			},
		},
		{
			name:      "Valid HTTP",
			probeType: "http",
			address:   "http://google.com",
			wantErr:   false,
			check: func(t *testing.T, c Config) {
				if c.Type != "http" {
					t.Errorf("expected type http, got %s", c.Type)
				}
				if c.Command != "" {
					t.Errorf("expected empty command for http, got %s", c.Command)
				}
			},
		},
		{
			name:      "Valid DNS",
			probeType: "dns",
			address:   "8.8.8.8",
			wantErr:   false,
			check: func(t *testing.T, c Config) {
				if c.Type != "dns" {
					t.Errorf("expected type dns, got %s", c.Type)
				}
				if c.Command != "" {
					t.Errorf("expected empty command for dns, got %s", c.Command)
				}
				if c.Address != "8.8.8.8" {
					t.Errorf("expected address 8.8.8.8, got %s", c.Address)
				}
			},
		},
		{
			name:      "Invalid Type",
			probeType: "rm -rf /",
			address:   "localhost",
			wantErr:   true,
			check:     nil,
		},
		{
			name:      "Valid Dig",
			probeType: "dig",
			address:   "8.8.8.8",
			wantErr:   false,
			check: func(t *testing.T, c Config) {
				if c.Type != "dig" {
					t.Errorf("expected type dig, got %s", c.Type)
				}
				if c.Command != "dig" {
					t.Errorf("expected command dig, got %s", c.Command)
				}
				// args should be @8.8.8.8 example.com A
				if len(c.Args) < 3 || c.Args[0] != "@8.8.8.8" {
					t.Errorf("expected arg @8.8.8.8, got %v", c.Args)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetConfig(tt.probeType, tt.address)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestRunDNS(t *testing.T) {
	// This test relies on external connectivity and a working DNS server at 8.8.8.8.
	// In a purely hermetic environment, this should be mocked, but for now we test broadly.
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	cfg := Config{
		Type:    "dns",
		Address: "8.8.8.8",
		Timeout: 2 * time.Second,
	}

	// We pass a context with timeout to Run mainly via cfg.Timeout, but Run creates its own child context.
	// Actually typical use is:
	// val, err := Run(cfg)

	start := time.Now()
	val, err := Run(cfg)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run(dns) failed: %v", err)
	}

	if val <= 0 {
		t.Errorf("expected positive latency, got %v", val)
	}

	// Verify we didn't just sleep and return 0
	t.Logf("DNS Probe took %v ns (%.2f ms), total test time %v", val, val/1e6, elapsed)
}

func TestRunDNS_LookupIP_Integration(t *testing.T) {
	// Explicitly verify runDNS acts as expected with the new code path
	// This calls the internal runDNS function if we export it or just via Run.
	// Since runDNS is unexported, we test via Run.

	cfg := Config{
		Type:    "dns",
		Address: "1.1.1.1", // Cloudflare
		Timeout: 2 * time.Second,
	}

	val, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run(dns) failed against 1.1.1.1: %v", err)
	}
	t.Logf("DNS Probe -> 1.1.1.1 took %.2f ms", val/1e6)
}

func TestRunDig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	// We need 'dig' installed for this to work.
	// Since we verified it in the previous step, we can try running it.
	// However, if the environment doesn't have it (e.g. CI without the docker image), it might fail.
	// We'll check if exec.LookPath finds it first.
	_, err := exec.LookPath("dig")
	if err != nil {
		t.Skip("dig not found in path")
	}

	cfg := Config{
		Type:    "dig",
		Address: "8.8.8.8",
		Timeout: 5 * time.Second,
	}

	// First ensure we interpret configuration correctly
	cfg, err = GetConfig(cfg.Type, cfg.Address)
	if err != nil {
		t.Fatalf("Failed to get config: %v", err)
	}
	cfg.Timeout = 5 * time.Second

	val, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run(dig) failed: %v", err)
	}

	if val <= 0 {
		t.Errorf("expected positive latency, got %v", val)
	}
	t.Logf("Dig Probe -> 8.8.8.8 took %.2f ms", val/1e6)
}
