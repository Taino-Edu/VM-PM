-- Rodadas do Cartola FC
CREATE TABLE IF NOT EXISTS rodadas (
    id          INTEGER PRIMARY KEY,
    nome        TEXT    NOT NULL,
    inicio      TEXT    NOT NULL,
    fim         TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'aguardando', -- aguardando | em_andamento | finalizada
    criado_em   TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Clubes
CREATE TABLE IF NOT EXISTS clubes (
    id          INTEGER PRIMARY KEY,
    nome        TEXT    NOT NULL,
    abreviacao  TEXT    NOT NULL,
    escudo_url  TEXT
);

-- Atletas (jogadores)
CREATE TABLE IF NOT EXISTS atletas (
    id              INTEGER PRIMARY KEY,
    apelido         TEXT    NOT NULL,
    nome            TEXT    NOT NULL,
    posicao         TEXT    NOT NULL, -- GOL | ZAG | LAT | MEI | ATA | TEC
    clube_id        INTEGER NOT NULL REFERENCES clubes(id),
    foto_url        TEXT,
    status          TEXT    NOT NULL DEFAULT 'Provável', -- Provável | Duvida | Suspenso | Contundido
    preco           REAL    NOT NULL DEFAULT 0.0,
    variacao_preco  REAL    NOT NULL DEFAULT 0.0,
    media_pontos    REAL    NOT NULL DEFAULT 0.0,
    jogos_disputados INTEGER NOT NULL DEFAULT 0,
    atualizado_em   TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Pontuações por rodada
CREATE TABLE IF NOT EXISTS pontuacoes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    atleta_id   INTEGER NOT NULL REFERENCES atletas(id),
    rodada_id   INTEGER NOT NULL REFERENCES rodadas(id),
    pontos      REAL    NOT NULL DEFAULT 0.0,
    scouts      TEXT,   -- JSON com os scouts da rodada
    criado_em   TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE(atleta_id, rodada_id)
);

-- Índices para queries frequentes da API
CREATE INDEX IF NOT EXISTS idx_atletas_posicao   ON atletas(posicao);
CREATE INDEX IF NOT EXISTS idx_atletas_clube      ON atletas(clube_id);
CREATE INDEX IF NOT EXISTS idx_pontuacoes_atleta  ON pontuacoes(atleta_id);
CREATE INDEX IF NOT EXISTS idx_pontuacoes_rodada  ON pontuacoes(rodada_id);
