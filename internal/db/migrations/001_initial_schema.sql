CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Users
CREATE TABLE users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username     TEXT NOT NULL UNIQUE,
    email        TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    elo          INTEGER NOT NULL DEFAULT 1000,  
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
 );

 -- Word list (ordered by id for consistent word progression)
CREATE TABLE word_list (
    id        SERIAL PRIMARY KEY,
    word      VARCHAR(5) NOT NULL UNIQUE,
    is_answer BOOLEAN NOT NULL DEFAULT false,
    is_valid  BOOLEAN NOT NULL DEFAULT true
);

CREATE INDEX idx_word_list_answer ON word_list (id) WHERE is_answer = true;

-- Matches
CREATE TABLE matches (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_a_id         UUID NOT NULL REFERENCES users(id),
    player_b_id         UUID NOT NULL REFERENCES users(id),
    status              TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending','active','finished')),
    winner_id           UUID REFERENCES users(id),
    player_a_hp         INTEGER NOT NULL DEFAULT 100,
    player_b_hp         INTEGER NOT NULL DEFAULT 100,
    player_a_word_idx   INTEGER NOT NULL DEFAULT 0,
    player_b_word_idx   INTEGER NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at         TIMESTAMPTZ,
    last_activity       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_matches_player_a ON matches (player_a_id);
CREATE INDEX idx_matches_player_b ON matches (player_b_id);
CREATE INDEX idx_matches_status   ON matches (status);

-- Guesses
CREATE TABLE guesses (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id   UUID NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id  UUID NOT NULL REFERENCES users(id),
    word_index INTEGER NOT NULL,
    guess      VARCHAR(5) NOT NULL,
    result     JSONB NOT NULL,
    guessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_guesses_match_player ON guesses (match_id, player_id);