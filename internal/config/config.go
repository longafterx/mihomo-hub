package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Server struct {
		Listen       string `yaml:"listen"`
		PublicOrigin string `yaml:"public_origin"`
		StaticDir    string `yaml:"static_dir"`
	} `yaml:"server"`
	Database struct {
		Path          string `yaml:"path"`
		BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
	} `yaml:"database"`
	Security struct {
		MasterKeyFile string `yaml:"master_key_file"`
		SessionHours  int    `yaml:"session_hours"`
	} `yaml:"security"`
	Gateway struct {
		AllowPublicControllers bool  `yaml:"allow_public_controllers"`
		AllowedPorts           []int `yaml:"allowed_ports"`
		AllowRuntimeWrites     bool  `yaml:"allow_runtime_writes"`
		RequestTimeoutSeconds  int   `yaml:"request_timeout_seconds"`
		MaxBodyBytes           int64 `yaml:"max_body_bytes"`
		MaxResponseBytes       int64 `yaml:"max_response_bytes"`
		MaxWSMessageBytes      int64 `yaml:"max_ws_message_bytes"`
		MaxWebSockets          int   `yaml:"max_websockets"`
	} `yaml:"gateway"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Collector struct {
		MaxInstances           int `yaml:"max_instances"`
		MaxConnections         int `yaml:"max_connections"`
		QueueCapacity          int `yaml:"queue_capacity"`
		BatchSize              int `yaml:"batch_size"`
		FlushIntervalMS        int `yaml:"flush_interval_ms"`
		SnapshotTimeoutSeconds int `yaml:"snapshot_timeout_seconds"`
		MaxSnapshotGapMS       int `yaml:"max_snapshot_gap_ms"`
		ReconnectMinMS         int `yaml:"reconnect_min_ms"`
		ReconnectMaxMS         int `yaml:"reconnect_max_ms"`
	} `yaml:"collector"`
	Retention struct {
		ConnectionsDays int `yaml:"connections_days"`
		MinuteStatsDays int `yaml:"minute_stats_days"`
		HourlyStatsDays int `yaml:"hourly_stats_days"`
	} `yaml:"retention"`
}

func Default() Config {
	var c Config
	c.Server.Listen = "127.0.0.1:8080"
	c.Server.PublicOrigin = "http://127.0.0.1:8080"
	c.Database.Path = "data/mihomo-hub.db"
	c.Database.BusyTimeoutMS = 5000
	c.Security.MasterKeyFile = "data/secrets/master.key"
	c.Security.SessionHours = 12
	c.Gateway.AllowedPorts = []int{}
	c.Gateway.AllowRuntimeWrites = true
	c.Gateway.RequestTimeoutSeconds = 30
	c.Gateway.MaxBodyBytes = 1 << 20
	c.Gateway.MaxResponseBytes = 16 << 20
	c.Gateway.MaxWSMessageBytes = 16 << 20
	c.Gateway.MaxWebSockets = 128
	c.Log.Level = "info"
	c.Collector.MaxInstances = 32
	c.Collector.MaxConnections = 10000
	c.Collector.QueueCapacity = 64
	c.Collector.BatchSize = 32
	c.Collector.FlushIntervalMS = 250
	c.Collector.SnapshotTimeoutSeconds = 15
	c.Collector.MaxSnapshotGapMS = 5000
	c.Collector.ReconnectMinMS = 1000
	c.Collector.ReconnectMaxMS = 60000
	c.Retention.ConnectionsDays = 14
	c.Retention.MinuteStatsDays = 60
	c.Retention.HourlyStatsDays = 365
	return c
}

func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return finish(c)
	}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	d := yaml.NewDecoder(io.LimitReader(f, 64<<10))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return c, errors.New("config must contain one YAML document")
	}
	return finish(c)
}

func finish(c Config) (Config, error) {
	stringsEnv := map[string]*string{
		"MIHOMOHUB_LISTEN":          &c.Server.Listen,
		"MIHOMOHUB_PUBLIC_ORIGIN":   &c.Server.PublicOrigin,
		"MIHOMOHUB_STATIC_DIR":      &c.Server.StaticDir,
		"MIHOMOHUB_DATABASE_PATH":   &c.Database.Path,
		"MIHOMOHUB_MASTER_KEY_FILE": &c.Security.MasterKeyFile,
		"MIHOMOHUB_LOG_LEVEL":       &c.Log.Level,
	}
	for name, field := range stringsEnv {
		if value, exists := os.LookupEnv(name); exists {
			*field = value
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("invalid server.listen: %w", err)
	}
	u, err := url.Parse(c.Server.PublicOrigin)
	if err != nil || u == nil {
		return errors.New("invalid server.public_origin")
	}
	validScheme := u.Scheme == "http" || u.Scheme == "https"
	hasExtras := u.User != nil || u.RawQuery != "" || u.Fragment != ""
	if !validScheme || u.Host == "" || u.Path != "" || hasExtras {
		return errors.New("server.public_origin must be an http(s) origin without path or credentials")
	}
	if c.Database.Path == "" || c.Security.MasterKeyFile == "" {
		return errors.New("database path and master key file are required")
	}
	validSession := c.Security.SessionHours >= 1 && c.Security.SessionHours <= 168
	validTimeout := c.Gateway.RequestTimeoutSeconds >= 1 && c.Gateway.RequestTimeoutSeconds <= 300
	validBusy := c.Database.BusyTimeoutMS >= 1 && c.Database.BusyTimeoutMS <= 60000
	if !validSession || !validTimeout || !validBusy {
		return errors.New("session or timeout outside allowed range")
	}
	limits := []int64{c.Gateway.MaxBodyBytes, c.Gateway.MaxResponseBytes, c.Gateway.MaxWSMessageBytes}
	for _, limit := range limits {
		if limit < 1 || limit > 64<<20 {
			return errors.New("body and message limits must be between 1 and 64 MiB")
		}
	}
	if c.Gateway.MaxWebSockets < 1 || c.Gateway.MaxWebSockets > 4096 {
		return errors.New("max_websockets must be between 1 and 4096")
	}
	for _, p := range c.Gateway.AllowedPorts {
		if p < 1 || p > 65535 {
			return errors.New("allowed_ports contains an invalid TCP port: " + strconv.Itoa(p))
		}
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(c.Log.Level))); err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	validCollector := c.Collector.MaxInstances >= 1 && c.Collector.MaxInstances <= 256 && c.Collector.MaxConnections >= 1
	validBounds := c.Collector.MaxConnections <= 100000 && c.Collector.QueueCapacity >= 1 && c.Collector.QueueCapacity <= 4096
	validBatch := c.Collector.BatchSize >= 1 && c.Collector.BatchSize <= 256 && c.Collector.FlushIntervalMS >= 1
	validPeriods := c.Collector.FlushIntervalMS <= 5000 && c.Collector.SnapshotTimeoutSeconds >= 1 && c.Collector.SnapshotTimeoutSeconds <= 300
	validGap := c.Collector.MaxSnapshotGapMS >= 100 && c.Collector.MaxSnapshotGapMS <= 300000
	validReconnect := c.Collector.ReconnectMinMS >= 1 && c.Collector.ReconnectMaxMS >= c.Collector.ReconnectMinMS && c.Collector.ReconnectMaxMS <= 300000
	if !validCollector || !validBounds || !validBatch || !validPeriods || !validGap || !validReconnect {
		return errors.New("invalid collector capacity or timing")
	}
	for _, days := range []int{c.Retention.ConnectionsDays, c.Retention.MinuteStatsDays, c.Retention.HourlyStatsDays} {
		if days < 1 || days > 3650 {
			return errors.New("retention days must be between 1 and 3650")
		}
	}
	if c.Retention.HourlyStatsDays < c.Retention.MinuteStatsDays {
		return errors.New("hourly retention must cover minute retention")
	}
	return nil
}
