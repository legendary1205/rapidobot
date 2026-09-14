-- Admins added from inside the bot. The owners in ADMIN_IDS are not stored
-- here: they come from the environment on every start, so no action inside
-- the bot can ever remove the last way in.
CREATE TABLE admins (
    user_id    INTEGER PRIMARY KEY,
    added_by   INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
