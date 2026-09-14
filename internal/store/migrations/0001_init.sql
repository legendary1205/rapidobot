-- All money is whole toman, stored as INTEGER - never floating point.
-- All timestamps are unix seconds.

CREATE TABLE users (
    id                INTEGER PRIMARY KEY,            -- the Telegram user id
    username          TEXT    NOT NULL DEFAULT '',
    first_name        TEXT    NOT NULL DEFAULT '',
    balance           INTEGER NOT NULL DEFAULT 0 CHECK (balance >= 0),
    is_agent          INTEGER NOT NULL DEFAULT 0,
    is_blocked        INTEGER NOT NULL DEFAULT 0,
    referral_code     TEXT    NOT NULL UNIQUE,
    referred_by       INTEGER REFERENCES users(id),
    -- Set once the referrer has been paid for this user, so a second
    -- purchase can never pay the same reward twice.
    referral_rewarded INTEGER NOT NULL DEFAULT 0,
    trial_used        INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL
);

CREATE TABLE plans (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    data_gb     INTEGER NOT NULL CHECK (data_gb >= 0),   -- 0 = unlimited
    days        INTEGER NOT NULL CHECK (days >= 0),      -- 0 = no expiry
    price       INTEGER NOT NULL CHECK (price >= 0),
    agent_price INTEGER NOT NULL DEFAULT 0,              -- 0 = same as price
    is_active   INTEGER NOT NULL DEFAULT 1,
    sort        INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

-- One row per account this bot created on the panel.
CREATE TABLE services (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id        INTEGER NOT NULL REFERENCES users(id),
    panel_username TEXT    NOT NULL UNIQUE,
    plan_id        INTEGER REFERENCES plans(id),
    is_trial       INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL
);
CREATE INDEX services_user_idx ON services (user_id);

CREATE TABLE orders (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id),
    kind            TEXT    NOT NULL CHECK (kind IN ('purchase', 'renew', 'topup')),
    plan_id         INTEGER REFERENCES plans(id),
    service_id      INTEGER REFERENCES services(id),    -- the service a renewal extends
    base_amount     INTEGER NOT NULL,                   -- before any discount
    discount        INTEGER NOT NULL DEFAULT 0,
    amount          INTEGER NOT NULL CHECK (amount >= 0),
    discount_code   TEXT,
    pay_method      TEXT    CHECK (pay_method IN ('card', 'wallet')),
    -- draft            -> chosen, not paid
    -- awaiting_receipt -> card payment picked, waiting for the photo
    -- awaiting_review  -> receipt in, waiting for an admin
    -- processing       -> paid and being delivered; only one worker ever
    --                     wins the move into this state
    -- completed | rejected | failed | cancelled -> terminal
    status          TEXT    NOT NULL CHECK (status IN
                        ('draft', 'awaiting_receipt', 'awaiting_review', 'processing',
                         'completed', 'rejected', 'failed', 'cancelled')),
    receipt_file_id TEXT,
    decided_by      INTEGER,
    error           TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX orders_user_idx   ON orders (user_id, status);
CREATE INDEX orders_status_idx ON orders (status);

CREATE TABLE discount_codes (
    code       TEXT    PRIMARY KEY COLLATE NOCASE,
    percent    INTEGER NOT NULL DEFAULT 0 CHECK (percent BETWEEN 0 AND 100),
    amount     INTEGER NOT NULL DEFAULT 0 CHECK (amount >= 0),
    max_uses   INTEGER NOT NULL DEFAULT 0,   -- 0 = unlimited
    used       INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL DEFAULT 0,   -- 0 = never
    is_active  INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

-- A code is spent once per customer; the primary key enforces it.
CREATE TABLE discount_uses (
    code     TEXT    NOT NULL COLLATE NOCASE,
    user_id  INTEGER NOT NULL,
    order_id INTEGER NOT NULL,
    used_at  INTEGER NOT NULL,
    PRIMARY KEY (code, user_id)
);

-- Every change to a balance, so a balance is always explainable.
CREATE TABLE wallet_ledger (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    delta      INTEGER NOT NULL,
    reason     TEXT    NOT NULL,
    order_id   INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX wallet_ledger_user_idx ON wallet_ledger (user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
