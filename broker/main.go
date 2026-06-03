package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ── Modelos ──────────────────────────────────────────────────────────────────

type Payload struct {
	Fonte   string             `json:"fonte"`
	Rodada  int                `json:"rodada"`
	Atletas []AtletaPayload    `json:"atletas"`
	Clubes  []ClubePayload     `json:"clubes"`
}

type AtletaPayload struct {
	ID              int     `json:"id"`
	Apelido         string  `json:"apelido"`
	Nome            string  `json:"nome"`
	Posicao         string  `json:"posicao"`
	ClubeID         int     `json:"clube_id"`
	FotoURL         *string `json:"foto_url"`
	Status          string  `json:"status"`
	Preco           float64 `json:"preco"`
	VariacaoPreco   float64 `json:"variacao_preco"`
	MediaPontos     float64 `json:"media_pontos"`
	JogosDisputados int     `json:"jogos_disputados"`
}

type ClubePayload struct {
	ID         int     `json:"id"`
	Nome       string  `json:"nome"`
	Abreviacao string  `json:"abreviacao"`
	EscudoURL  *string `json:"escudo_url"`
}

// ── Broker ───────────────────────────────────────────────────────────────────

type Broker struct {
	db    *sql.DB
	queue chan Payload
}

func NewBroker(db *sql.DB) *Broker {
	b := &Broker{
		db:    db,
		queue: make(chan Payload, 100), // buffer de 100 payloads
	}
	// Inicia workers de gravação
	for i := 0; i < 3; i++ {
		go b.worker(i)
	}
	return b
}

// worker consome a fila e persiste no SQLite
func (b *Broker) worker(id int) {
	slog.Info("Worker iniciado", "id", id)
	for payload := range b.queue {
		start := time.Now()
		if err := b.persist(payload); err != nil {
			slog.Error("Falha ao persistir payload", "err", err, "worker", id)
			continue
		}
		slog.Info("Payload persistido",
			"worker", id,
			"atletas", len(payload.Atletas),
			"rodada", payload.Rodada,
			"duracao", time.Since(start),
		)
	}
}

// persist grava atletas e clubes no SQLite dentro de uma transação
func (b *Broker) persist(p Payload) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Upsert clubes
	stmtClube, err := tx.Prepare(`
		INSERT INTO clubes (id, nome, abreviacao, escudo_url)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			nome       = excluded.nome,
			abreviacao = excluded.abreviacao,
			escudo_url = excluded.escudo_url
	`)
	if err != nil {
		return err
	}
	defer stmtClube.Close()

	for _, c := range p.Clubes {
		if _, err := stmtClube.Exec(c.ID, c.Nome, c.Abreviacao, c.EscudoURL); err != nil {
			return err
		}
	}

	// Upsert atletas
	stmtAtleta, err := tx.Prepare(`
		INSERT INTO atletas
			(id, apelido, nome, posicao, clube_id, foto_url, status,
			 preco, variacao_preco, media_pontos, jogos_disputados, atualizado_em)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			apelido          = excluded.apelido,
			posicao          = excluded.posicao,
			clube_id         = excluded.clube_id,
			foto_url         = excluded.foto_url,
			status           = excluded.status,
			preco            = excluded.preco,
			variacao_preco   = excluded.variacao_preco,
			media_pontos     = excluded.media_pontos,
			jogos_disputados = excluded.jogos_disputados,
			atualizado_em    = datetime('now')
	`)
	if err != nil {
		return err
	}
	defer stmtAtleta.Close()

	for _, a := range p.Atletas {
		if _, err := stmtAtleta.Exec(
			a.ID, a.Apelido, a.Nome, a.Posicao, a.ClubeID,
			a.FotoURL, a.Status, a.Preco, a.VariacaoPreco,
			a.MediaPontos, a.JogosDisputados,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ── HTTP Handler ─────────────────────────────────────────────────────────────

func (b *Broker) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload Payload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		slog.Warn("Payload inválido", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	select {
	case b.queue <- payload:
		w.WriteHeader(http.StatusAccepted) // 202 — enfileirado
	default:
		slog.Warn("Fila cheia, rejeitando payload")
		http.Error(w, "queue full", http.StatusServiceUnavailable)
	}
}

func (b *Broker) handleHealth(w http.ResponseWriter, r *http.Request) {
	var count int
	b.db.QueryRow("SELECT COUNT(*) FROM atletas").Scan(&count)
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"fila_pendente": len(b.queue),
		"atletas_db":    count,
	})
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/var/lib/megazord/cartola.db"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	slog.Info("Abrindo banco de dados", "path", dbPath)
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		slog.Error("Falha ao abrir banco", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Limita conexões para não sobrecarregar SQLite
	db.SetMaxOpenConns(1)

	broker := NewBroker(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/ingest", broker.handleIngest)
	mux.HandleFunc("/health", broker.handleHealth)

	addr := "127.0.0.1:" + port // escuta só localmente — não exposto para fora
	slog.Info("Broker ouvindo", "addr", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("Servidor encerrado", "err", err)
		os.Exit(1)
	}
}
