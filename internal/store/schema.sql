CREATE TABLE IF NOT EXISTS conversations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id    TEXT NOT NULL,
    model        TEXT NOT NULL,
    provider     TEXT NOT NULL,
    title        TEXT,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ,
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    seq          BIGSERIAL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    tool_calls      JSONB,
    token_count     INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    seq             BIGSERIAL
);

CREATE INDEX IF NOT EXISTS messages_conv_created_idx
    ON messages (conversation_id, created_at);

CREATE TABLE IF NOT EXISTS chunks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id      UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    chunk_index     INTEGER NOT NULL,
    content         TEXT NOT NULL,
    embedding       vector(384) NOT NULL,
    token_count     INTEGER NOT NULL DEFAULT 0,
    seq             BIGSERIAL
);

CREATE INDEX IF NOT EXISTS chunks_conv_idx ON chunks (conversation_id);

CREATE INDEX IF NOT EXISTS chunks_embedding_hnsw_idx
    ON chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX IF NOT EXISTS chunks_tsv_idx
    ON chunks USING gin (to_tsvector('english', content));

CREATE TABLE IF NOT EXISTS events (
    seq         BIGSERIAL PRIMARY KEY,
    entity_type TEXT NOT NULL,
    entity_id   UUID NOT NULL,
    op          TEXT NOT NULL,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS events_entity_idx ON events (entity_type, entity_id);
