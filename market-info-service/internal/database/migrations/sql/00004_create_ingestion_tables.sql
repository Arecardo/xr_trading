-- +goose Up
CREATE TABLE market_data.ingestion_runs (
    id uuid PRIMARY KEY,
    run_key varchar(192) NOT NULL,
    run_type varchar(24) NOT NULL,
    trigger_type varchar(16) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    scheduled_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    requested_by varchar(128),
    task_count integer NOT NULL DEFAULT 0,
    success_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    context jsonb NOT NULL DEFAULT '{}',
    error_summary jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_ingestion_runs_key UNIQUE (run_key),
    CONSTRAINT ck_ingestion_runs_type CHECK (
        run_type IN ('incremental', 'backfill', 'repair', 'revision')
    ),
    CONSTRAINT ck_ingestion_runs_trigger CHECK (
        trigger_type IN ('scheduler', 'manual', 'recovery')
    ),
    CONSTRAINT ck_ingestion_runs_status CHECK (
        status IN ('pending', 'running', 'partial', 'success', 'failed', 'canceled')
    ),
    CONSTRAINT ck_ingestion_runs_counts CHECK (
        task_count >= 0 AND success_count >= 0 AND failed_count >= 0
    )
);

CREATE TABLE market_data.ingestion_tasks (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES market_data.ingestion_runs(id),
    subscription_id uuid NOT NULL REFERENCES market_data.collection_subscriptions(id),
    retry_of_task_id uuid REFERENCES market_data.ingestion_tasks(id),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    next_attempt_at timestamptz,
    locked_by varchar(128),
    locked_until timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    provider_request_id varchar(128),
    error_code varchar(64),
    error_message text,
    error_details jsonb NOT NULL DEFAULT '{}',
    canceled_by varchar(128),
    cancel_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_ingestion_task_range UNIQUE (
        run_id, subscription_id, range_start, range_end
    ),
    CONSTRAINT ck_ingestion_tasks_range CHECK (range_end > range_start),
    CONSTRAINT ck_ingestion_tasks_attempts CHECK (
        attempt_count >= 0 AND max_attempts > 0
    ),
    CONSTRAINT ck_ingestion_tasks_status CHECK (
        status IN ('pending', 'running', 'retry_wait', 'success', 'failed', 'canceled')
    )
);
CREATE INDEX idx_ingestion_tasks_claim
    ON market_data.ingestion_tasks (status, next_attempt_at, created_at)
    WHERE status IN ('pending', 'retry_wait');
CREATE INDEX idx_ingestion_tasks_lease
    ON market_data.ingestion_tasks (locked_until)
    WHERE status = 'running';
CREATE INDEX idx_ingestion_tasks_retry_of
    ON market_data.ingestion_tasks (retry_of_task_id)
    WHERE retry_of_task_id IS NOT NULL;
CREATE UNIQUE INDEX uq_active_manual_retry
    ON market_data.ingestion_tasks (retry_of_task_id)
    WHERE retry_of_task_id IS NOT NULL
      AND status IN ('pending', 'running', 'retry_wait');

CREATE TABLE market_data.ingestion_checkpoints (
    subscription_id uuid PRIMARY KEY REFERENCES market_data.collection_subscriptions(id),
    last_success_open_time timestamptz,
    last_closed_open_time timestamptz,
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_ingestion_checkpoints_failures CHECK (consecutive_failures >= 0)
);

CREATE TABLE market_data.data_quality_issues (
    id uuid PRIMARY KEY,
    instrument_id uuid NOT NULL REFERENCES core.instruments(id),
    provider_instrument_id uuid REFERENCES market_data.provider_instruments(id),
    interval varchar(8),
    open_time timestamptz,
    rule_code varchar(64) NOT NULL,
    severity varchar(16) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'open',
    summary varchar(512) NOT NULL,
    details jsonb NOT NULL DEFAULT '{}',
    detected_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolution_note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_quality_issues_interval CHECK (interval IS NULL OR interval IN ('1h', '1d')),
    CONSTRAINT ck_quality_issues_severity CHECK (
        severity IN ('info', 'warning', 'error', 'critical')
    ),
    CONSTRAINT ck_quality_issues_status CHECK (
        status IN ('open', 'acknowledged', 'resolved', 'ignored')
    )
);
CREATE UNIQUE INDEX uq_open_quality_issue
    ON market_data.data_quality_issues (
        instrument_id, provider_instrument_id, interval, open_time, rule_code
    ) NULLS NOT DISTINCT
    WHERE status IN ('open', 'acknowledged');

-- +goose Down
DROP TABLE IF EXISTS market_data.data_quality_issues;
DROP TABLE IF EXISTS market_data.ingestion_checkpoints;
DROP TABLE IF EXISTS market_data.ingestion_tasks;
DROP TABLE IF EXISTS market_data.ingestion_runs;
