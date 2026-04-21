ALTER TABLE users
ADD COLUMN IF NOT EXISTS auth_provider TEXT NOT NULL DEFAULT 'local',
ADD COLUMN IF NOT EXISTS provider_user_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_auth_provider_user_id
ON users (auth_provider, provider_user_id)
WHERE provider_user_id IS NOT NULL;