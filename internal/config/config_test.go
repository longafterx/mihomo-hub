package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/config"
)

func TestLoad(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		yaml string
		ok   bool
	}{
		{name: "partial config uses secure defaults", yaml: "log:\n  level: debug\n", ok: true},
		{name: "unknown field", yaml: "server:\n  typo: true\n"},
		{name: "multiple documents", yaml: "log: {}\n---\nlog: {}"},
		{name: "credentials in origin", yaml: "server:\n  public_origin: http://user:pass@localhost"},
		{name: "zero limit", yaml: "gateway:\n  max_body_bytes: 0"},
		{name: "bad port", yaml: "gateway:\n  allowed_ports: [65536]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := config.Load(path)
			if (err == nil) != tt.ok {
				t.Fatalf("Load error = %v, want success %v", err, tt.ok)
			}
			if tt.ok && c.Server.Listen != "127.0.0.1:8080" {
				t.Fatal("default loopback listener lost")
			}
		})
	}
}

func TestCollectorAndRetentionValidation(t *testing.T) {
	for _, modify := range []func(*config.Config){
		func(c *config.Config) { c.Collector.QueueCapacity = 0 },
		func(c *config.Config) { c.Collector.MaxInstances = 257 },
		func(c *config.Config) { c.Collector.MaxConnections = 100001 },
		func(c *config.Config) { c.Collector.ReconnectMaxMS = 1 },
		func(c *config.Config) { c.Retention.ConnectionsDays = 0 },
		func(c *config.Config) { c.Retention.HourlyStatsDays = 1 },
	} {
		c := config.Default()
		modify(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("invalid collector/retention accepted")
		}
	}
}
