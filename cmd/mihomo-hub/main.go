package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/web"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mihomo-hub stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "", "optional YAML configuration file")
	output := flags.String("output", "", "new SQLite backup destination")
	username := flags.String("username", "admin", "administrator username for init")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional argument")
	}
	if command == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": app.Version, "backend_commit": app.Commit, "frontend_commit": app.FrontendCommit, "frontend_embedded": web.Embedded, "schema_version": storage.SchemaVersion, "build_time": app.BuildTime})
	}
	c, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Log.Level)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch command {
	case "init":
		return initialize(ctx, c, *username)
	case "backup":
		if *output == "" {
			return errors.New("backup requires --output pointing to a new file")
		}
		db, err := storage.Open(ctx, c.Database.Path, c.Database.BusyTimeoutMS)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := (storage.Telemetry{DB: db}).Backup(ctx, *output); err != nil {
			return err
		}
		logger.Info("consistent database backup created; back up the master key separately")
		return nil
	case "healthcheck":
		host, port, err := net.SplitHostPort(c.Server.Listen)
		if err != nil {
			return err
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{}}
		response, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
		if err != nil {
			return errors.New("server not ready")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return errors.New("server not ready")
		}
		return nil
	case "serve":
		return serve(ctx, c, logger)
	default:
		return errors.New("expected init, serve, backup, version or healthcheck command")
	}
}

func initialize(ctx context.Context, c config.Config, username string) error {
	if strings.TrimSpace(username) == "" || len(username) > 128 {
		return errors.New("invalid administrator username")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("init requires an interactive terminal; passwords are never accepted in argv or environment")
	}
	db, err := storage.Open(ctx, c.Database.Path, c.Database.BusyTimeoutMS)
	if err != nil {
		return err
	}
	defer db.Close()
	admins := storage.Admins{DB: db}
	if _, err := admins.Admin(ctx); err == nil {
		return errors.New("administrator already initialized")
	}
	fmt.Fprintln(os.Stderr, "设置 MihomoHub 网页登录密码，与 Controller Secret 无关。")
	fmt.Fprintf(os.Stderr, "Admin password (at least %d bytes): ", auth.MinPasswordBytes)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirmation, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if string(password) != string(confirmation) {
		return errors.New("passwords do not match")
	}
	hash, err := auth.HashPassword(string(password))
	clear(password)
	clear(confirmation)
	if err != nil {
		return err
	}
	if _, err := os.Stat(c.Security.MasterKeyFile); errors.Is(err, os.ErrNotExist) {
		if err := storage.InitKey(c.Security.MasterKeyFile); err != nil {
			return err
		}
	}
	if _, err := storage.LoadKey(c.Security.MasterKeyFile); err != nil {
		return err
	}
	if err := admins.Initialize(ctx, auth.Admin{Username: username, PasswordHash: hash}); err != nil {
		return err
	}
	slog.Info("administrator initialized; store the master key backup separately from the database")
	return nil
}

func serve(ctx context.Context, c config.Config, logger *slog.Logger) error {
	a, err := app.Open(ctx, c, logger)
	if err != nil {
		return err
	}
	defer a.Close()
	srv := &http.Server{
		Addr: c.Server.Listen, Handler: a.Handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	logger.Info("server starting", "listen", c.Server.Listen)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Shutdown must outlive the canceled signal context.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return errors.Join(err, srv.Close())
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
