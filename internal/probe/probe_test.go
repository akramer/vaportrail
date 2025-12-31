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
