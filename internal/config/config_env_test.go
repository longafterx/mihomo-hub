package config_test

import (
	"testing"

	"github.com/longafterx/mihomo-hub/internal/config"
)

func TestEnvironmentOverrides(t *testing.T) {
	t.Setenv("MIHOMOHUB_LISTEN", "127.0.0.1:18080")
	t.Setenv("MIHOMOHUB_PUBLIC_ORIGIN", "https://hub.example")
	c, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != "127.0.0.1:18080" || c.Server.PublicOrigin != "https://hub.example" {
		t.Fatal("environment was not applied")
	}
}
