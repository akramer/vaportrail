package probe

import (
	"testing"
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
				if c.Command != "curl" {
					t.Errorf("expected command curl, got %s", c.Command)
				}
			},
		},
		{
			name:      "Valid DNS",
			probeType: "dns",
			address:   "google.com",
			wantErr:   false,
			check: func(t *testing.T, c Config) {
				if c.Command != "dig" {
					t.Errorf("expected command dig, got %s", c.Command)
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
