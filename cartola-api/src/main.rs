use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::Json,
    routing::get,
    Router,
};
use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use std::sync::{Arc, Mutex};
use tracing::info;

// ── Estado compartilhado ────────────────────────────────────────────────────

#[derive(Clone)]
struct AppState {
    db: Arc<Mutex<Connection>>,
}

// ── Modelos de resposta ─────────────────────────────────────────────────────

#[derive(Debug, Serialize)]
struct AtletaResp {
    id:               i64,
    apelido:          String,
    posicao:          String,
    clube:            String,
    preco:            f64,
    variacao_preco:   f64,
    media_pontos:     f64,
    jogos_disputados: i64,
    status:           String,
    foto_url:         Option<String>,
    tendencia:        Tendencia,
}

#[derive(Debug, Serialize)]
struct Tendencia {
    // Últimas 5 rodadas
    ultimas_pontuacoes: Vec<f64>,
    media_recente:      f64,
    em_alta:            bool,
}

#[derive(Debug, Serialize)]
struct HealthResp {
    status:  &'static str,
    versao:  &'static str,
    rodada:  Option<i64>,
}

// ── Query params ────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct AtletasQuery {
    posicao:  Option<String>,
    clube_id: Option<i64>,
    limite:   Option<i64>,
}

// ── Handlers ────────────────────────────────────────────────────────────────

async fn health(State(state): State<AppState>) -> Json<HealthResp> {
    let rodada = {
        let db = state.db.lock().unwrap();
        db.query_row(
            "SELECT id FROM rodadas WHERE status = 'em_andamento' LIMIT 1",
            [],
            |r| r.get(0),
        ).ok()
    };

    Json(HealthResp {
        status: "ok",
        versao: env!("CARGO_PKG_VERSION"),
        rodada,
    })
}

async fn listar_atletas(
    State(state): State<AppState>,
    Query(q): Query<AtletasQuery>,
) -> Result<Json<Vec<AtletaResp>>, StatusCode> {
    let db = state.db.lock().map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    let limite = q.limite.unwrap_or(50).min(200);

    let mut sql = String::from(
        "SELECT a.id, a.apelido, a.posicao, c.nome as clube, a.preco, \
         a.variacao_preco, a.media_pontos, a.jogos_disputados, a.status, a.foto_url \
         FROM atletas a \
         JOIN clubes c ON c.id = a.clube_id \
         WHERE 1=1"
    );

    if q.posicao.is_some()  { sql.push_str(" AND a.posicao = ?1"); }
    if q.clube_id.is_some() { sql.push_str(" AND a.clube_id = ?2"); }
    sql.push_str(" ORDER BY a.media_pontos DESC LIMIT ?3");

    let mut stmt = db.prepare(&sql).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    let posicao_param  = q.posicao.unwrap_or_default();
    let clube_param    = q.clube_id.unwrap_or(0);

    let atletas = stmt.query_map(
        rusqlite::params![posicao_param, clube_param, limite],
        |row| {
            Ok((
                row.get::<_, i64>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, f64>(4)?,
                row.get::<_, f64>(5)?,
                row.get::<_, f64>(6)?,
                row.get::<_, i64>(7)?,
                row.get::<_, String>(8)?,
                row.get::<_, Option<String>>(9)?,
            ))
        }
    ).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    let mut resultado = Vec::new();
    for atleta in atletas {
        let (id, apelido, posicao, clube, preco, variacao, media, jogos, status, foto) =
            atleta.map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

        let tendencia = calcular_tendencia(&db, id);

        resultado.push(AtletaResp {
            id,
            apelido,
            posicao,
            clube,
            preco,
            variacao_preco: variacao,
            media_pontos:   media,
            jogos_disputados: jogos,
            status,
            foto_url: foto,
            tendencia,
        });
    }

    Ok(Json(resultado))
}

async fn detalhe_atleta(
    State(state): State<AppState>,
    Path(id): Path<i64>,
) -> Result<Json<AtletaResp>, StatusCode> {
    let db = state.db.lock().map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    let atleta = db.query_row(
        "SELECT a.id, a.apelido, a.posicao, c.nome, a.preco, \
         a.variacao_preco, a.media_pontos, a.jogos_disputados, a.status, a.foto_url \
         FROM atletas a JOIN clubes c ON c.id = a.clube_id WHERE a.id = ?1",
        [id],
        |row| Ok((
            row.get::<_, i64>(0)?,
            row.get::<_, String>(1)?,
            row.get::<_, String>(2)?,
            row.get::<_, String>(3)?,
            row.get::<_, f64>(4)?,
            row.get::<_, f64>(5)?,
            row.get::<_, f64>(6)?,
            row.get::<_, i64>(7)?,
            row.get::<_, String>(8)?,
            row.get::<_, Option<String>>(9)?,
        )),
    ).map_err(|_| StatusCode::NOT_FOUND)?;

    let tendencia = calcular_tendencia(&db, atleta.0);

    Ok(Json(AtletaResp {
        id:               atleta.0,
        apelido:          atleta.1,
        posicao:          atleta.2,
        clube:            atleta.3,
        preco:            atleta.4,
        variacao_preco:   atleta.5,
        media_pontos:     atleta.6,
        jogos_disputados: atleta.7,
        status:           atleta.8,
        foto_url:         atleta.9,
        tendencia,
    }))
}

// ── Cálculo de tendência (lógica de negócio no servidor — RF-C04) ───────────

fn calcular_tendencia(db: &Connection, atleta_id: i64) -> Tendencia {
    let mut stmt = db.prepare(
        "SELECT p.pontos FROM pontuacoes p \
         JOIN rodadas r ON r.id = p.rodada_id \
         WHERE p.atleta_id = ?1 \
         ORDER BY r.id DESC LIMIT 5"
    ).unwrap();

    let pontos: Vec<f64> = stmt
        .query_map([atleta_id], |r| r.get(0))
        .unwrap()
        .filter_map(|r| r.ok())
        .collect();

    let media_recente = if pontos.is_empty() {
        0.0
    } else {
        pontos.iter().sum::<f64>() / pontos.len() as f64
    };

    // "Em alta" se a última pontuação supera a média das anteriores
    let em_alta = pontos.first().map(|&ultima| {
        let anteriores: Vec<f64> = pontos.iter().skip(1).copied().collect();
        if anteriores.is_empty() { return false; }
        let media_ant = anteriores.iter().sum::<f64>() / anteriores.len() as f64;
        ultima > media_ant
    }).unwrap_or(false);

    Tendencia {
        ultimas_pontuacoes: pontos,
        media_recente,
        em_alta,
    }
}

// ── Entrada ─────────────────────────────────────────────────────────────────

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(std::env::var("RUST_LOG").unwrap_or_else(|_| "info".into()))
        .init();

    let db_path = std::env::var("DB_PATH")
        .unwrap_or_else(|_| "/var/lib/megazord/cartola.db".to_string());

    let port = std::env::var("PORT")
        .unwrap_or_else(|_| "8080".to_string());

    info!("Abrindo banco de dados em {}", db_path);
    let conn = Connection::open(&db_path)?;

    // Executa migrations na inicialização
    conn.execute_batch(include_str!("../../migrations/001_initial.sql"))?;

    let state = AppState {
        db: Arc::new(Mutex::new(conn)),
    };

    let app = Router::new()
        .route("/health",          get(health))
        .route("/atletas",         get(listar_atletas))
        .route("/atletas/:id",     get(detalhe_atleta))
        .with_state(state);

    let addr = format!("0.0.0.0:{}", port);
    info!("cartola-api ouvindo em {}", addr);

    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}
