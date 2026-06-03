package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ── Constantes ────────────────────────────────────────────────────────────────

const (
	binDir    = "/opt/megazord"      // onde os binários ficam instalados
	deployDir = "/tmp/megazord-deploy" // staging temporário
	maxUpload = 150 << 20             // 150MB max por release
)

// Ordem de restart: deployer sempre por último (reinicia a si mesmo)
var services = []string{
	"broker",
	"cartola-api",
	"watchdog",
	"deployer",
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	token := os.Getenv("DEPLOY_TOKEN")
	if token == "" {
		slog.Error("DEPLOY_TOKEN não configurado — abortando")
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/deploy", requireBearer(token, handleDeploy))
	mux.HandleFunc("/health", handleHealth)

	addr := "127.0.0.1:" + port
	slog.Info("Deployer ouvindo", "addr", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("Deployer encerrado", "err", err)
		os.Exit(1)
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		slog.Error("Payload muito grande ou inválido", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("release")
	if err != nil {
		slog.Error("Campo 'release' ausente", "err", err)
		http.Error(w, "missing release file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	slog.Info("Release recebido", "filename", header.Filename, "size_bytes", header.Size)

	// Retorna 202 imediatamente — deploy acontece em background
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "deploying"})

	// Copia o conteúdo antes de fechar o request
	tmp, err := os.CreateTemp("", "release-*.tar.gz")
	if err != nil {
		slog.Error("Falha ao criar arquivo temporário", "err", err)
		return
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		slog.Error("Falha ao salvar release", "err", err)
		return
	}
	tmp.Close()

	go func() {
		defer os.Remove(tmpPath)
		if err := deploy(tmpPath); err != nil {
			slog.Error("Deploy falhou", "err", err)
		}
	}()
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ── Deploy ────────────────────────────────────────────────────────────────────

func deploy(tarPath string) error {
	slog.Info("Iniciando deploy", "arquivo", tarPath)

	// Limpa e recria diretório de staging
	os.RemoveAll(deployDir)
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		return err
	}

	// Extrai os binários
	if err := extractTarGz(tarPath, deployDir); err != nil {
		return err
	}

	// Garante que /opt/megazord existe
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	// Copia cada binário para o destino final
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(deployDir, entry.Name())
		dst := filepath.Join(binDir, entry.Name())
		slog.Info("Instalando binário", "nome", entry.Name(), "destino", dst)
		if err := installBin(src, dst); err != nil {
			slog.Error("Falha ao instalar binário", "nome", entry.Name(), "err", err)
			continue
		}
	}

	// Reinicia serviços (deployer por último — mata o próprio processo)
	for _, svc := range services {
		slog.Info("Reiniciando serviço", "service", svc)
		out, err := exec.Command("sudo", "systemctl", "restart", svc).CombinedOutput()
		if err != nil {
			slog.Error("Falha ao reiniciar", "service", svc, "err", err, "output", string(out))
		}
		// Pausa entre restarts para não sobrecarregar a VM
		time.Sleep(500 * time.Millisecond)
	}

	slog.Info("Deploy concluído com sucesso")
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// filepath.Base previne directory traversal
		target := filepath.Join(dest, filepath.Base(hdr.Name))
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

func installBin(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Escreve em arquivo temporário ao lado do destino e depois renomeia
	// (evita corrupção se o processo cair no meio)
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()

	return os.Rename(tmp, dst) // atômico no Linux
}

func requireBearer(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			slog.Warn("Requisição não autorizada", "ip", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
