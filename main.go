// station-mcp: MCP server that controls a rooted Yandex Station 2 over SSH or Luna HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ilodezis/yandex-station-mcp/internal/mcpserver"
	"github.com/ilodezis/yandex-station-mcp/internal/oauth"
	"github.com/ilodezis/yandex-station-mcp/internal/quiet"
	"github.com/ilodezis/yandex-station-mcp/internal/service"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

var (
	gitCommit = "dev"
	buildDate = "unknown"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// oauthFromEnv builds the claude.ai OAuth gate. It is off (nil) while none of its variables are
// set, so deployments from before OAuth keep starting; a half-set config is an error.
func oauthFromEnv() (*oauth.Server, error) {
	pass, sec, csec := os.Getenv("OAUTH_PASSWORD"), os.Getenv("OAUTH_TOKEN_SECRET"), os.Getenv("OAUTH_CLIENT_SECRET")
	if pass == "" && sec == "" && csec == "" {
		return nil, nil
	}
	days, err := envInt("OAUTH_TOKEN_TTL_DAYS", oauth.DefaultTokenTTL/86400)
	if err != nil {
		return nil, err
	}
	return oauth.NewServer(oauth.Config{
		Password: pass, TokenSecret: sec, ClientSecret: csec,
		ClientID:  env("OAUTH_CLIENT_ID", ""),
		PublicURL: env("PUBLIC_URL", ""),
		TokenTTL:  int64(days) * 86400,
	})
}

// buildStation instantiates the configured backend (SSH or Luna) and returns the Station.
func buildStation(cfg station.Config) (*station.Station, error) {
	backendType := strings.ToLower(strings.TrimSpace(env("STATION_BACKEND", "ssh")))
	switch backendType {
	case "luna":
		lunaAddr := station.ResolveLunaAddr(env("LUNA_ADDR", ""), env("STATION_ADDR", "127.0.0.1:2222"))
		lunaBackend, err := station.NewLunaBackend(station.LunaConfig{
			BaseURL: lunaAddr,
		}, cfg)
		if err != nil {
			return nil, err
		}
		log.Printf("station-mcp using luna backend at %s", lunaAddr)
		return station.NewWithBackend(lunaBackend, cfg), nil
	case "ssh", "":
		runner, err := station.NewSSHRunner(station.SSHConfig{
			Addr:       env("STATION_ADDR", "127.0.0.1:2222"),
			User:       env("STATION_USER", "root"),
			KeyFile:    env("STATION_KEY_FILE", "/run/secrets/station_key"),
			HostKeySHA: env("STATION_HOST_KEY", ""),
		})
		if err != nil {
			return nil, err
		}
		log.Printf("station-mcp using ssh backend at %s", env("STATION_ADDR", "127.0.0.1:2222"))
		return station.New(runner, cfg), nil
	default:
		return nil, fmt.Errorf("unknown STATION_BACKEND %q (allowed: ssh, luna)", backendType)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		return healthCheck(env("ADDR", ":8150"))
	}

	maxVol, err := envInt("MAX_VOLUME", 80)
	if err != nil {
		return err
	}
	maxLED, err := envInt("LED_MAX_LEVEL", 128)
	if err != nil {
		return err
	}
	budget, err := envInt("VOICE_BUDGET", 10)
	if err != nil {
		return err
	}

	cfg := station.DefaultConfig()
	cfg.MaxVolume, cfg.MaxLEDLevel = maxVol, maxLED
	st, err := buildStation(cfg)
	if err != nil {
		return err
	}

	q, err := quiet.NewClock(env("QUIET_FROM", "23:00"), env("QUIET_TO", "08:00"), env("TIMEZONE", "Europe/Moscow"))
	if err != nil {
		return err
	}
	ledOn := envBool("LED_ENABLED", true)
	svc := service.New(st, q, service.Options{LEDEnabled: ledOn, VoiceBudget: budget})

	tokens := map[mcpserver.Profile]string{
		mcpserver.Agent: os.Getenv("TOKEN_AGENT"),
		mcpserver.Brain: os.Getenv("TOKEN_BRAIN"),
	}
	if len(strings.TrimSpace(tokens[mcpserver.Agent])) < 24 {
		return errors.New("TOKEN_AGENT is required and must be at least 24 characters")
	}
	if t := tokens[mcpserver.Brain]; t != "" && len(t) < 24 {
		return errors.New("TOKEN_BRAIN must be at least 24 characters")
	}
	opt := mcpserver.Options{LEDEnabled: ledOn, Version: gitCommit + " " + buildDate}
	servers := map[mcpserver.Profile]*mcp.Server{}
	for p := range tokens {
		servers[p] = mcpserver.New(p, svc, opt)
	}

	auth := mcpserver.NewAuth(tokens)
	handler := mcpserver.Handler(auth, servers)
	gate, err := oauthFromEnv()
	if err != nil {
		return err
	}
	if gate != nil {
		auth.EnableOAuth(gate.TokenValid, gate.Challenge)
		mux := http.NewServeMux()
		gate.Register(mux)
		mux.Handle("/", handler)
		handler = mux
		log.Print("OAuth gate on: claude.ai connector may connect")
	}

	addr := env("ADDR", ":8150")
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("station-mcp %s listening on %s (quiet %s-%s %s)", gitCommit, addr,
		env("QUIET_FROM", "23:00"), env("QUIET_TO", "08:00"), env("TIMEZONE", "Europe/Moscow"))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthCheck is the container HEALTHCHECK: the runtime image has no curl.
func healthCheck(addr string) error {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + host + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "station-mcp:", err)
		os.Exit(1)
	}
}
