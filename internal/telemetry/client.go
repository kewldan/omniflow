package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxPayloadBytes = 64 << 10

type Config struct {
	Enabled          bool
	Endpoint         string
	Version          string
	Service          string
	InstallationID   string
	CollectorEnabled bool
	DatabaseURL      string
}

type Event struct {
	Schema       int               `json:"schema"`
	Installation string            `json:"installation_id"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Service      string            `json:"service"`
	OS           string            `json:"os"`
	Architecture string            `json:"architecture"`
	Features     map[string]bool   `json:"features,omitempty"`
	Counters     map[string]uint64 `json:"counters,omitempty"`
	OccurredAt   time.Time         `json:"occurred_at"`
}

type Client struct {
	logger      *slog.Logger
	cfg         Config
	http        *http.Client
	id          string
	once        sync.Once
	collectorDB *pgxpool.Pool
}

func NewClient(logger *slog.Logger, cfg Config) *Client {
	client := &Client{
		logger: logger,
		cfg:    cfg,
		http:   &http.Client{Timeout: 5 * time.Second},
		id:     cfg.InstallationID,
	}
	if cfg.CollectorEnabled && cfg.DatabaseURL != "" {
		pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
		if err != nil {
			logger.Error("telemetry collector database unavailable", "error", err)
		} else {
			client.collectorDB = pool
		}
	}
	return client
}

func (client *Client) Close() {
	if client.collectorDB != nil {
		client.collectorDB.Close()
	}
}

func ResolveInstallationID(ctx context.Context, databaseURL string, logger *slog.Logger) string {
	if databaseURL == "" {
		logger.Warn("telemetry installation ID is ephemeral because database URL is empty")
		return randomID()
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Warn("telemetry installation ID is ephemeral", "error", err)
		return randomID()
	}
	defer pool.Close()
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO telemetry_installation (singleton)
		VALUES (true)
		ON CONFLICT (singleton) DO UPDATE SET singleton = EXCLUDED.singleton
		RETURNING installation_id::text
	`).Scan(&id)
	if err != nil {
		logger.Warn("telemetry installation ID is ephemeral", "error", err)
		return randomID()
	}
	return id
}

func (client *Client) Start(ctx context.Context) {
	client.once.Do(func() {
		if client.collectorDB != nil {
			go client.enforceRetention(ctx)
		}
		if !client.cfg.Enabled {
			client.logger.Info("anonymous telemetry disabled")
			return
		}
		client.logger.Info("anonymous telemetry enabled", "disable_with", "APP_TELEMETRY_ENABLED=false")
		go client.heartbeat(ctx)
	})
}

func (client *Client) enforceRetention(ctx context.Context) {
	client.deleteExpiredEvents(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			client.deleteExpiredEvents(ctx)
		}
	}
}

func (client *Client) deleteExpiredEvents(ctx context.Context) {
	if _, err := client.collectorDB.Exec(ctx, "DELETE FROM telemetry_events WHERE received_at < now() - interval '180 days'"); err != nil && ctx.Err() == nil {
		client.logger.Error("telemetry retention failed", "error", err)
	}
}

func (client *Client) heartbeat(ctx context.Context) {
	client.send(ctx, Event{Name: "installation.heartbeat"})
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			client.send(ctx, Event{Name: "installation.heartbeat"})
		}
	}
}

func (client *Client) send(ctx context.Context, event Event) {
	event.Schema = 1
	event.Installation = client.id
	event.Version = client.cfg.Version
	event.Service = client.cfg.Service
	event.OS = runtime.GOOS
	event.Architecture = runtime.GOARCH
	event.OccurredAt = time.Now().UTC()
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func (client *Client) CollectorHandler() http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var event Event
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxPayloadBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil || validateEvent(event) != nil {
			http.Error(writer, "invalid telemetry event", http.StatusBadRequest)
			return
		}
		if client.collectorDB == nil {
			http.Error(writer, "collector unavailable", http.StatusServiceUnavailable)
			return
		}
		features, _ := json.Marshal(event.Features)
		counters, _ := json.Marshal(event.Counters)
		_, err := client.collectorDB.Exec(request.Context(), `
			INSERT INTO telemetry_events (
				installation_id, name, version, service, os, architecture,
				features, counters, occurred_at
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9)
		`, event.Installation, event.Name, event.Version, event.Service, event.OS, event.Architecture, features, counters, event.OccurredAt)
		if err != nil {
			client.logger.Error("telemetry event persistence failed", "error", err)
			http.Error(writer, "collector unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}
}

func validateEvent(event Event) error {
	if event.Schema != 1 || len(event.Installation) != 36 {
		return errors.New("invalid envelope")
	}
	if event.Name != "installation.heartbeat" && event.Name != "feature.usage" {
		return errors.New("invalid event name")
	}
	if len(event.Version) > 32 || len(event.Service) > 16 || len(event.OS) > 32 || len(event.Architecture) > 32 {
		return errors.New("field too long")
	}
	if len(event.Features) > 64 || len(event.Counters) > 64 {
		return errors.New("too many metrics")
	}
	for key := range event.Features {
		if !safeMetricKey(key) {
			return errors.New("invalid feature key")
		}
	}
	for key := range event.Counters {
		if !safeMetricKey(key) {
			return errors.New("invalid counter key")
		}
	}
	if event.OccurredAt.IsZero() || time.Since(event.OccurredAt) > 7*24*time.Hour || time.Until(event.OccurredAt) > time.Hour {
		return errors.New("invalid event time")
	}
	return nil
}

func safeMetricKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for _, char := range key {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' {
			return false
		}
	}
	return true
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(errors.New("secure random source unavailable"))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
}
