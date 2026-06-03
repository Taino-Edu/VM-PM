use anyhow::Result;
use serde::{Deserialize, Serialize};
use tracing::{info, error};

// ── Modelos da API do Cartola FC ────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct MercadoResponse {
    atletas: Vec<AtletaCartola>,
    clubes: std::collections::HashMap<String, ClubeCartola>,
    rodada_atual: u32,
}

#[derive(Debug, Deserialize)]
struct AtletaCartola {
    atleta_id:       u32,
    apelido:         String,
    nome:            String,
    posicao_id:      u32,
    clube_id:        u32,
    foto:            Option<String>,
    status_id:       u32,
    preco_num:       f64,
    variacao_num:    f64,
    media_num:       f64,
    jogos_num:       u32,
}

#[derive(Debug, Deserialize)]
struct ClubeCartola {
    id:         u32,
    nome:       String,
    abreviacao: String,
    escudos:    std::collections::HashMap<String, String>,
}

// ── Payload enviado ao Broker ───────────────────────────────────────────────

#[derive(Debug, Serialize)]
struct BrokerPayload {
    fonte:    String,
    rodada:   u32,
    atletas:  Vec<AtletaNormalizado>,
    clubes:   Vec<ClubeNormalizado>,
}

#[derive(Debug, Serialize)]
struct AtletaNormalizado {
    id:              u32,
    apelido:         String,
    nome:            String,
    posicao:         String,
    clube_id:        u32,
    foto_url:        Option<String>,
    status:          String,
    preco:           f64,
    variacao_preco:  f64,
    media_pontos:    f64,
    jogos_disputados: u32,
}

#[derive(Debug, Serialize)]
struct ClubeNormalizado {
    id:         u32,
    nome:       String,
    abreviacao: String,
    escudo_url: Option<String>,
}

// ── Mapeamentos ─────────────────────────────────────────────────────────────

fn posicao_nome(id: u32) -> &'static str {
    match id {
        1 => "GOL",
        2 => "ZAG",
        3 => "LAT",
        4 => "MEI",
        5 => "ATA",
        6 => "TEC",
        _ => "???",
    }
}

fn status_nome(id: u32) -> &'static str {
    match id {
        2 => "Duvida",
        3 => "Suspenso",
        5 => "Contundido",
        6 => "Nulo",
        7 => "Provável",
        _ => "Provável",
    }
}

// ── Lógica principal ────────────────────────────────────────────────────────

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(std::env::var("RUST_LOG").unwrap_or_else(|_| "info".into()))
        .init();

    let broker_url = std::env::var("BROKER_URL")
        .unwrap_or_else(|_| "http://127.0.0.1:8081/ingest".to_string());

    info!("Iniciando scraper do Cartola FC...");

    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .user_agent("megazord-scraper/0.1")
        .build()?;

    info!("Buscando dados do mercado...");
    let resp = client
        .get("https://api.cartola.globo.com/atletas/mercado")
        .send()
        .await?;

    if !resp.status().is_success() {
        error!("API Cartola retornou status {}", resp.status());
        anyhow::bail!("Falha ao buscar mercado");
    }

    let mercado: MercadoResponse = resp.json().await?;
    info!(
        "Recebidos {} atletas, {} clubes — rodada {}",
        mercado.atletas.len(),
        mercado.clubes.len(),
        mercado.rodada_atual
    );

    // Normaliza clubes
    let clubes: Vec<ClubeNormalizado> = mercado.clubes.values().map(|c| {
        ClubeNormalizado {
            id:         c.id,
            nome:       c.nome.clone(),
            abreviacao: c.abreviacao.clone(),
            escudo_url: c.escudos.get("60x60").cloned(),
        }
    }).collect();

    // Normaliza atletas
    let atletas: Vec<AtletaNormalizado> = mercado.atletas.into_iter().map(|a| {
        AtletaNormalizado {
            id:               a.atleta_id,
            apelido:          a.apelido,
            nome:             a.nome,
            posicao:          posicao_nome(a.posicao_id).to_string(),
            clube_id:         a.clube_id,
            foto_url:         a.foto,
            status:           status_nome(a.status_id).to_string(),
            preco:            a.preco_num,
            variacao_preco:   a.variacao_num,
            media_pontos:     a.media_num,
            jogos_disputados: a.jogos_num,
        }
    }).collect();

    let payload = BrokerPayload {
        fonte:   "cartola-fc".to_string(),
        rodada:  mercado.rodada_atual,
        atletas,
        clubes,
    };

    info!("Enviando payload para o broker em {}...", broker_url);
    let resp = client
        .post(&broker_url)
        .json(&payload)
        .send()
        .await?;

    if resp.status().is_success() {
        info!("Payload entregue ao broker com sucesso.");
    } else {
        error!("Broker recusou o payload: {}", resp.status());
        anyhow::bail!("Broker retornou erro");
    }

    Ok(())
}
