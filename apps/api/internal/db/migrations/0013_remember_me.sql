-- Opt-in persistence never extends an existing session or owner step-up.
ALTER TABLE sessions ADD COLUMN remembered boolean NOT NULL DEFAULT false;
