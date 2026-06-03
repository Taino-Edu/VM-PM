package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ── Configuração ─────────────────────────────────────────────────────────────

type endpoint struct {
	name string
	url  string
}

type serviceState struct {
	up        bool
	failCount int
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	webhook := os.Getenv("DISCORD_WEBHOOK")
	intervalSec := getEnvInt("CHECK_INTERVAL", 60)

	endpoints := []endpoint{
		{"cartola-api", "http://127.0.0.1:8080/health"},
		{"broker",      "http://127.0.0.1:8081/health"},
		{"caddy",       "https://megazordvmpm.duckdns.org"},
	}

	states := make(map[string]*serviceState, len(endpoints))
	for _, e := range endpoints {
		// Assume que os serviços começam offline — notifica quando subirem
		states[e.name] = &serviceState{up: false}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	slog.Info("Watchdog iniciado",
		"endpoints", len(endpoints),
		"intervalo_s", intervalSec,
	)

	// Self health endpoint
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		slog.Info("Watchdog health endpoint", "addr", "127.0.0.1:8083")
		if err := http.ListenAndServe("127.0.0.1:8083", mux); err != nil {
			slog.Error("Health endpoint encerrado", "err", err)
		}
	}()

	// Loop principal de checagem
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	// Checa imediatamente na inicialização
	for _, ep := range endpoints {
		check(client, ep, states[ep.name], webhook)
	}

	for range ticker.C {
		for _, ep := range endpoints {
			check(client, ep, states[ep.name], webhook)
		}
	}
}

// ── Lógica de checagem ────────────────────────────────────────────────────────

func check(client *http.Client, ep endpoint, state *serviceState, webhook string) {
	resp, err := client.Get(ep.url)
	isUp := err == nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}

	switch {
	case isUp && !state.up:
		// Serviço voltou
		state.up = true
		state.failCount = 0
		slog.Info("Serviço recuperado", "name", ep.name)
		notify(webhook, "✅ **"+ep.name+"** voltou ao ar!", 0x57F287)

	case !isUp:
		// Serviço continua/ficou fora
		state.failCount++
		if state.up || state.failCount == 1 {
			state.up = false
			slog.Error("Serviço indisponível", "name", ep.name, "falhas", state.failCount)
			notify(webhook, "🔴 **"+ep.name+"** está fora do ar!", 0xED4245)
		}

	default:
		// Serviço ok
		slog.Debug("OK", "name", ep.name)
		state.failCount = 0
	}
}

// ── Discord ───────────────────────────────────────────────────────────────────

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Description string `json:"description"`
	Color       int    `json:"color"`
}

func notify(webhookURL, msg string, color int) {
	if webhookURL == "" {
		slog.Warn("DISCORD_WEBHOOK não configurado — notificação ignorada")
		return
	}

	payload := discordPayload{
		Embeds: []discordEmbed{{Description: msg, Color: color}},
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("Falha ao notificar Discord", "err", err)
		return
	}
	resp.Body.Close()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func getEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
