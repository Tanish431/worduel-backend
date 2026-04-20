CREATE TABLE rooms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        CHAR(6) NOT NULL UNIQUE,
    host_id     UUID NOT NULL REFERENCES users(id),
    status      TEXT NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'matched')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW() 
);

CREATE INDEX idx_rooms_code ON rooms (code);