-- Invariants live here, not only in Go (ADR 0004). Every check below costs
-- an index lookup at most; nothing scans the ledger.

CREATE FUNCTION reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '%: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'check_violation';
END;
$$;

-- Wallets -------------------------------------------------------------------

CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor BIGINT      NOT NULL CHECK (balance_minor >= 0),
    version       BIGINT      NOT NULL CHECK (version >= 1),
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_key UNIQUE (player_id, currency),
    CONSTRAINT wallets_id_currency_key UNIQUE (id, currency)
);

CREATE TRIGGER wallets_no_delete BEFORE DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER wallets_no_truncate BEFORE TRUNCATE ON wallets
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- Wager transactions --------------------------------------------------------

CREATE TABLE wager_transactions (
    id                    UUID        PRIMARY KEY,
    origin                TEXT        NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    kind                  TEXT        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status                TEXT        NOT NULL CHECK (status IN ('PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    wallet_id             UUID        NOT NULL,
    player_id             UUID        NOT NULL,
    currency              CHAR(3)     NOT NULL,
    amount_minor          BIGINT      NOT NULL CHECK (amount_minor >= 0),
    provider_id           TEXT,
    external_id           TEXT,
    idempotency_key       TEXT,
    payload_hash          BYTEA,
    round_id              TEXT,
    game_id               TEXT,
    reference_external_id TEXT,
    reference_tx_id       UUID        REFERENCES wager_transactions (id),
    failure_code          TEXT,
    result_balance_minor  BIGINT,
    result_wallet_version BIGINT,
    correlation_id        TEXT        NOT NULL,
    attempts              INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at       TIMESTAMPTZ,
    deadline_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL,
    completed_at          TIMESTAMPTZ,

    CONSTRAINT wager_tx_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),

    CONSTRAINT wager_tx_origin_shape CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING'
            AND provider_id IS NULL AND external_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_id IS NULL AND reference_tx_id IS NULL)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_id IS NOT NULL AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL AND octet_length(payload_hash) = 32
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),

    CONSTRAINT wager_tx_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),

    CONSTRAINT wager_tx_reference_required CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_id IS NOT NULL
    ),

    CONSTRAINT wager_tx_status_shape CHECK (
        (status = 'PENDING_REFERENCE' AND completed_at IS NULL AND failure_code IS NULL
            AND reference_external_id IS NOT NULL AND next_attempt_at IS NOT NULL AND deadline_at IS NOT NULL)
        OR (status = 'PROCESSED' AND completed_at IS NOT NULL AND failure_code IS NULL
            AND result_balance_minor IS NOT NULL AND result_wallet_version IS NOT NULL)
        OR (status = 'REJECTED' AND completed_at IS NOT NULL AND failure_code IS NOT NULL
            AND result_balance_minor IS NOT NULL AND result_wallet_version IS NOT NULL)
        OR (status = 'FAILED' AND completed_at IS NOT NULL AND failure_code IS NOT NULL)
    )
);

CREATE UNIQUE INDEX wager_tx_external_key ON wager_transactions (provider_id, external_id)
    WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_idempotency_key ON wager_transactions (provider_id, idempotency_key)
    WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_single_opening ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';
-- REFUND and ROLLBACK compete for the same slot (ADR 0005).
CREATE UNIQUE INDEX wager_tx_single_reversal ON wager_transactions (reference_tx_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');
CREATE INDEX wager_tx_pending_due ON wager_transactions (next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_tx_pending_by_reference ON wager_transactions (provider_id, reference_external_id)
    WHERE status = 'PENDING_REFERENCE';

CREATE FUNCTION wager_transactions_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager_transactions: rows cannot be deleted'
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transactions: % is terminal', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.currency, NEW.amount_minor,
        NEW.provider_id, NEW.external_id, NEW.idempotency_key, NEW.payload_hash, NEW.round_id,
        NEW.game_id, NEW.reference_external_id, NEW.correlation_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.currency, OLD.amount_minor,
        OLD.provider_id, OLD.external_id, OLD.idempotency_key, OLD.payload_hash, OLD.round_id,
        OLD.game_id, OLD.reference_external_id, OLD.correlation_id, OLD.created_at) THEN
        RAISE EXCEPTION 'wager_transactions: identity of % is immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER wager_transactions_guard BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();
CREATE TRIGGER wager_transactions_no_truncate BEFORE TRUNCATE ON wager_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- Ledger --------------------------------------------------------------------

CREATE TABLE wallet_ledger_entries (
    id             UUID        PRIMARY KEY,
    seq            BIGINT      GENERATED ALWAYS AS IDENTITY,
    wallet_id      UUID        NOT NULL REFERENCES wallets (id),
    transaction_id UUID        NOT NULL REFERENCES wager_transactions (id),
    direction      TEXT        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount_minor   BIGINT      NOT NULL CHECK (amount_minor > 0),
    currency       CHAR(3)     NOT NULL,
    balance_before BIGINT      NOT NULL CHECK (balance_before >= 0),
    balance_after  BIGINT      NOT NULL CHECK (balance_after >= 0),
    wallet_version BIGINT      NOT NULL CHECK (wallet_version >= 1),
    created_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_seq_key UNIQUE (seq),
    CONSTRAINT ledger_arithmetic CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount_minor)
        OR (direction = 'DEBIT' AND balance_after = balance_before - amount_minor)
    ),
    CONSTRAINT ledger_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_transaction_key UNIQUE (transaction_id),
    CONSTRAINT ledger_wallet_version_key UNIQUE (wallet_id, wallet_version)
);

-- Checks the entry against its transaction (direction by kind) and against the
-- already updated wallet (balance, version, chain). Index lookups only.
CREATE FUNCTION ledger_entry_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    tx             wager_transactions%ROWTYPE;
    w              wallets%ROWTYPE;
    expected       TEXT;
    ref_direction  TEXT;
    previous_after BIGINT;
BEGIN
    SELECT * INTO tx FROM wager_transactions WHERE id = NEW.transaction_id;
    IF NOT FOUND OR tx.status <> 'PROCESSED' THEN
        RAISE EXCEPTION 'ledger: transaction % is not processed', NEW.transaction_id
            USING ERRCODE = 'check_violation';
    END IF;
    IF tx.wallet_id <> NEW.wallet_id OR tx.currency <> NEW.currency OR tx.amount_minor <> NEW.amount_minor THEN
        RAISE EXCEPTION 'ledger: entry does not match transaction %', tx.id
            USING ERRCODE = 'check_violation';
    END IF;

    expected := CASE tx.kind
        WHEN 'BET' THEN 'DEBIT'
        WHEN 'WIN' THEN 'CREDIT'
        WHEN 'REFUND' THEN 'CREDIT'
        WHEN 'OPENING' THEN 'CREDIT'
        ELSE NULL
    END;
    IF tx.kind = 'ROLLBACK' THEN
        SELECT direction INTO ref_direction
          FROM wallet_ledger_entries WHERE transaction_id = tx.reference_tx_id;
        IF FOUND THEN
            expected := CASE ref_direction WHEN 'DEBIT' THEN 'CREDIT' ELSE 'DEBIT' END;
        END IF;
    END IF;
    IF expected IS NULL OR NEW.direction <> expected THEN
        RAISE EXCEPTION 'ledger: % is not a valid direction for % %', NEW.direction, tx.kind, tx.id
            USING ERRCODE = 'check_violation';
    END IF;

    SELECT * INTO w FROM wallets WHERE id = NEW.wallet_id;
    IF w.balance_minor <> NEW.balance_after OR w.version <> NEW.wallet_version THEN
        RAISE EXCEPTION 'ledger: entry does not match the current state of wallet %', w.id
            USING ERRCODE = 'check_violation';
    END IF;

    SELECT balance_after INTO previous_after
      FROM wallet_ledger_entries
     WHERE wallet_id = NEW.wallet_id AND wallet_version = NEW.wallet_version - 1;
    IF FOUND THEN
        IF previous_after <> NEW.balance_before THEN
            RAISE EXCEPTION 'ledger: chain broken for wallet %', w.id
                USING ERRCODE = 'check_violation';
        END IF;
    ELSIF EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE wallet_id = NEW.wallet_id) THEN
        RAISE EXCEPTION 'ledger: chain gap for wallet %', w.id
            USING ERRCODE = 'check_violation';
    ELSIF NEW.balance_before <> 0 THEN
        RAISE EXCEPTION 'ledger: first entry of wallet % must start at zero', w.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER ledger_entry_guard BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entry_guard();
CREATE TRIGGER ledger_no_update_delete BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- At commit, every balance change must be proven by the entry of its version.
CREATE FUNCTION wallet_balance_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    entry_after BIGINT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'wallet %: must be created at version 1', NEW.id
                USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.balance_minor = 0 THEN
            RETURN NULL;
        END IF;
    ELSE
        IF NEW.balance_minor = OLD.balance_minor AND NEW.version = OLD.version THEN
            RETURN NULL;
        END IF;
        IF NEW.version <> OLD.version + 1 THEN
            RAISE EXCEPTION 'wallet %: version must advance by exactly one', NEW.id
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    SELECT balance_after INTO entry_after
      FROM wallet_ledger_entries
     WHERE wallet_id = NEW.id AND wallet_version = NEW.version;
    IF NOT FOUND OR entry_after <> NEW.balance_minor THEN
        RAISE EXCEPTION 'wallet %: balance at version % has no matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallets_balance_guard
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_balance_guard();

-- Inbox ---------------------------------------------------------------------

CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   BYTEA       NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions (id),
    outcome        TEXT        NOT NULL,
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TRIGGER inbox_no_update_delete BEFORE UPDATE OR DELETE ON inbox_messages
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER inbox_no_truncate BEFORE TRUNCATE ON inbox_messages
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- Outbox --------------------------------------------------------------------

CREATE TABLE outbox_events (
    seq              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id         UUID        NOT NULL UNIQUE,
    partition_key    TEXT        NOT NULL,
    event_type       TEXT        NOT NULL,
    aggregate_id     TEXT        NOT NULL,
    payload          JSONB       NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    attempts         INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  TIMESTAMPTZ NOT NULL,
    claim_id         UUID,
    claim_expires_at TIMESTAMPTZ,
    published_at     TIMESTAMPTZ,
    dead_at          TIMESTAMPTZ,
    last_error       TEXT
);

CREATE INDEX outbox_unpublished_by_partition ON outbox_events (partition_key, seq)
    WHERE published_at IS NULL AND dead_at IS NULL;

CREATE FUNCTION outbox_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'outbox_events: rows cannot be deleted'
            USING ERRCODE = 'check_violation';
    END IF;
    IF (NEW.seq, NEW.event_id, NEW.partition_key, NEW.event_type, NEW.aggregate_id, NEW.payload, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.seq, OLD.event_id, OLD.partition_key, OLD.event_type, OLD.aggregate_id, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox_events: snapshot of % is immutable', OLD.event_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_events_guard BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();
CREATE TRIGGER outbox_no_truncate BEFORE TRUNCATE ON outbox_events
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- Runtime role: least privilege, never owner --------------------------------

GRANT USAGE ON SCHEMA public TO wager_app;
GRANT SELECT, INSERT ON wallets, wager_transactions, wallet_ledger_entries, inbox_messages, outbox_events TO wager_app;
GRANT USAGE ON SEQUENCE wallet_ledger_entries_seq_seq, outbox_events_seq_seq TO wager_app;
GRANT UPDATE (balance_minor, version, updated_at) ON wallets TO wager_app;
GRANT UPDATE (status, reference_tx_id, failure_code, result_balance_minor, result_wallet_version,
              attempts, next_attempt_at, deadline_at, updated_at, completed_at)
    ON wager_transactions TO wager_app;
GRANT UPDATE (attempts, next_attempt_at, claim_id, claim_expires_at, published_at, dead_at, last_error)
    ON outbox_events TO wager_app;
