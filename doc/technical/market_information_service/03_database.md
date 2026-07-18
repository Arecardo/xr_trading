# 市场资讯服务数据库设计

> 来源：拆分自 `../11_market_information_database.md`。

## 1. 目标与原则

本设计覆盖市场资讯服务第一阶段所需的资产主数据、供应商映射、最新行情、`1h`/`1d` K 线、采集任务和数据质量记录。

- 使用 PostgreSQL 15 或更高版本，同一数据库内按 `core` 与 `market_data` schema 隔离。
- 领域实体和任务使用应用生成的 UUIDv7，数据库列使用原生 `uuid` 类型。
- 所有外键使用 UUID；可读 `code` 只用于配置、API、日志和人工定位。
- `code`、市场 `symbol` 与供应商 `external_symbol` 分别保存。
- 价格、数量和成交量使用 `numeric(38,18)`，不使用二进制浮点。
- 时间使用 `timestamptz` 并统一写入 UTC。
- 状态使用 `varchar + CHECK`，避免 PostgreSQL ENUM 带来的演进成本。
- 不稳定的供应商扩展字段使用 `jsonb`，稳定且需要查询的字段必须结构化。
- 第一阶段不对行情表分区，不引入 Redis、Kafka 或外部任务队列。

## 2. Schema 与数据所有权

```text
core
  assets
  instruments
  asset_aliases
  instrument_aliases

market_data
  providers
  provider_instruments
  collection_subscriptions
  market_bars
  latest_quotes
  ingestion_runs
  ingestion_tasks
  ingestion_checkpoints
  data_quality_issues
```

- 独立核心资产目录拥有并迁移 `core` schema；市场资讯服务的生产 migration 不创建或修改它。
- 市场资讯服务对核心主数据只读，对 `market_data` schema 读写。
- 本地与 CI 使用独立 bootstrap fixture 创建最小 `core` 前置结构，不混入生产 migration 历史。
- `provider_instruments` 仅描述行情供应商映射，不承载下单权限和交易能力。
- 交易服务需要独立维护交易侧券商映射。

## 3. 核心主数据表

### 3.1 core.assets

```sql
CREATE TABLE core.assets (
    id                  uuid PRIMARY KEY,
    code                varchar(128) NOT NULL,
    asset_type          varchar(16) NOT NULL,
    canonical_symbol    varchar(64) NOT NULL,
    name                varchar(255) NOT NULL,
    status              varchar(16) NOT NULL DEFAULT 'active',
    metadata            jsonb NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_assets_code UNIQUE (code),
    CONSTRAINT ck_assets_code CHECK (code = lower(code)),
    CONSTRAINT ck_assets_type
        CHECK (asset_type IN ('STOCK', 'ETF', 'CRYPTO', 'CASH')),
    CONSTRAINT ck_assets_status
        CHECK (status IN ('active', 'inactive', 'delisted'))
);

CREATE INDEX idx_assets_symbol
    ON core.assets (canonical_symbol);

CREATE INDEX idx_assets_type_status
    ON core.assets (asset_type, status);
```

不对 `asset_type + canonical_symbol` 建立唯一约束，因为不同市场可能出现相同代码。

### 3.2 core.instruments

`asset_id` 表示现货交易品种的基础经济资产。第一阶段不重复保存 `base_asset_id`；未来引入衍生品时再增加语义明确的 `underlying_asset_id`。

```sql
CREATE TABLE core.instruments (
    id                  uuid PRIMARY KEY,
    code                varchar(160) NOT NULL,
    asset_id            uuid NOT NULL,
    venue               varchar(32) NOT NULL,
    instrument_type     varchar(24) NOT NULL,
    symbol              varchar(64) NOT NULL,
    quote_asset_id      uuid,
    quote_currency      varchar(16) NOT NULL,
    market_timezone     varchar(64) NOT NULL,
    price_scale         smallint,
    quantity_scale      smallint,
    lot_size            numeric(38,18),
    min_quantity        numeric(38,18),
    status              varchar(16) NOT NULL DEFAULT 'active',
    valid_from          timestamptz,
    valid_to            timestamptz,
    metadata            jsonb NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_instruments_asset
        FOREIGN KEY (asset_id) REFERENCES core.assets(id),
    CONSTRAINT fk_instruments_quote_asset
        FOREIGN KEY (quote_asset_id) REFERENCES core.assets(id),
    CONSTRAINT uq_instruments_code UNIQUE (code),
    CONSTRAINT ck_instruments_code CHECK (code = lower(code)),
    CONSTRAINT ck_instruments_type
        CHECK (instrument_type IN ('EQUITY', 'ETF', 'SPOT')),
    CONSTRAINT ck_instruments_status
        CHECK (status IN ('active', 'suspended', 'delisted')),
    CONSTRAINT ck_instruments_valid_range
        CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    CONSTRAINT ck_instruments_price_scale
        CHECK (price_scale IS NULL OR price_scale BETWEEN 0 AND 18),
    CONSTRAINT ck_instruments_quantity_scale
        CHECK (quantity_scale IS NULL OR quantity_scale BETWEEN 0 AND 18),
    CONSTRAINT ck_instruments_lot_size
        CHECK (lot_size IS NULL OR lot_size > 0),
    CONSTRAINT ck_instruments_min_quantity
        CHECK (min_quantity IS NULL OR min_quantity > 0)
);

CREATE UNIQUE INDEX uq_active_instrument_market_symbol
    ON core.instruments (venue, instrument_type, symbol)
    WHERE valid_to IS NULL;

CREATE INDEX idx_instruments_asset
    ON core.instruments (asset_id, status);
```

### 3.3 core.asset_aliases

```sql
CREATE TABLE core.asset_aliases (
    id              uuid PRIMARY KEY,
    asset_id        uuid NOT NULL,
    alias_code      varchar(128) NOT NULL,
    valid_from      timestamptz,
    valid_to        timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_asset_aliases_asset
        FOREIGN KEY (asset_id) REFERENCES core.assets(id),
    CONSTRAINT uq_asset_aliases_code UNIQUE (alias_code),
    CONSTRAINT ck_asset_aliases_code CHECK (alias_code = lower(alias_code)),
    CONSTRAINT ck_asset_aliases_valid_range
        CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from)
);
```

### 3.4 core.instrument_aliases

```sql
CREATE TABLE core.instrument_aliases (
    id              uuid PRIMARY KEY,
    instrument_id   uuid NOT NULL,
    alias_code      varchar(160) NOT NULL,
    valid_from      timestamptz,
    valid_to        timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_instrument_aliases_instrument
        FOREIGN KEY (instrument_id) REFERENCES core.instruments(id),
    CONSTRAINT uq_instrument_aliases_code UNIQUE (alias_code),
    CONSTRAINT ck_instrument_aliases_code CHECK (alias_code = lower(alias_code)),
    CONSTRAINT ck_instrument_aliases_valid_range
        CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from)
);
```

别名拆表而不使用多态 `entity_aliases`，以便 PostgreSQL 真实执行外键约束。供应商历史代码通过 `provider_instruments.valid_to` 管理，不进入别名表。

## 4. 供应商与采集配置

### 4.1 market_data.providers

```sql
CREATE TABLE market_data.providers (
    id              uuid PRIMARY KEY,
    code            varchar(64) NOT NULL,
    name            varchar(128) NOT NULL,
    provider_type   varchar(24) NOT NULL,
    status          varchar(16) NOT NULL DEFAULT 'active',
    metadata        jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_providers_code UNIQUE (code),
    CONSTRAINT ck_providers_code CHECK (code = lower(code)),
    CONSTRAINT ck_providers_type
        CHECK (provider_type IN ('BROKER', 'EXCHANGE', 'AGGREGATOR')),
    CONSTRAINT ck_providers_status
        CHECK (status IN ('active', 'disabled', 'degraded'))
);
```

第一阶段包含 `longbridge` 与 `bybit`。API Key、Secret 和签名材料不进入该表。

### 4.2 market_data.provider_instruments

```sql
CREATE TABLE market_data.provider_instruments (
    id                  uuid PRIMARY KEY,
    code                varchar(192) NOT NULL,
    provider_id         uuid NOT NULL,
    instrument_id       uuid NOT NULL,
    external_symbol     varchar(128) NOT NULL,
    provider_market     varchar(32) NOT NULL,
    capabilities        jsonb NOT NULL DEFAULT '{}',
    priority            smallint NOT NULL DEFAULT 100,
    is_default          boolean NOT NULL DEFAULT false,
    enabled             boolean NOT NULL DEFAULT true,
    valid_from          timestamptz,
    valid_to            timestamptz,
    metadata            jsonb NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_provider_instruments_provider
        FOREIGN KEY (provider_id) REFERENCES market_data.providers(id),
    CONSTRAINT fk_provider_instruments_instrument
        FOREIGN KEY (instrument_id) REFERENCES core.instruments(id),
    CONSTRAINT uq_provider_instruments_code UNIQUE (code),
    CONSTRAINT ck_provider_instruments_code CHECK (code = lower(code)),
    CONSTRAINT ck_provider_instruments_priority CHECK (priority >= 0),
    CONSTRAINT ck_provider_instruments_valid_range
        CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from)
);

CREATE UNIQUE INDEX uq_active_provider_external_symbol
    ON market_data.provider_instruments
       (provider_id, provider_market, external_symbol)
    WHERE valid_to IS NULL;

CREATE INDEX idx_provider_instruments_instrument
    ON market_data.provider_instruments (instrument_id, enabled);

CREATE UNIQUE INDEX uq_enabled_default_provider_instrument
    ON market_data.provider_instruments (instrument_id)
    WHERE enabled = true AND is_default = true AND valid_to IS NULL;
```

`priority` 数值越小优先级越高，用于没有显式默认来源时的稳定排序。`is_default` 用于 API 和前端为某个 Instrument 选择默认 Provider；部分唯一索引保证同一 Instrument 最多存在一个当前启用且仍有效的默认来源。

`capabilities` 示例：

```json
{
  "quote": true,
  "historical": true,
  "intervals": ["1h", "1d"]
}
```

### 4.3 market_data.collection_subscriptions

该表描述“系统准备采集什么”，与供应商理论支持能力分离。

```sql
CREATE TABLE market_data.collection_subscriptions (
    id                      uuid PRIMARY KEY,
    provider_instrument_id  uuid NOT NULL,
    interval                varchar(8) NOT NULL,
    enabled                 boolean NOT NULL DEFAULT true,
    priority                smallint NOT NULL DEFAULT 100,
    close_delay_seconds     integer NOT NULL DEFAULT 120,
    revision_delay_seconds  integer,
    metadata                jsonb NOT NULL DEFAULT '{}',
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_collection_subscription_mapping
        FOREIGN KEY (provider_instrument_id)
        REFERENCES market_data.provider_instruments(id),
    CONSTRAINT uq_collection_subscription
        UNIQUE (provider_instrument_id, interval),
    CONSTRAINT ck_collection_interval CHECK (interval IN ('1h', '1d')),
    CONSTRAINT ck_collection_priority CHECK (priority >= 0),
    CONSTRAINT ck_collection_close_delay CHECK (close_delay_seconds >= 0),
    CONSTRAINT ck_collection_revision_delay
        CHECK (revision_delay_seconds IS NULL OR revision_delay_seconds >= 0)
);
```

ADM-001 不新增表或 migration。订阅创建与修改的审计记录追加到 `metadata.audit_log` JSON 数组，每项固定包含 `action`、`requested_by`、`actor_type`、`request_id`、`reason` 和 UTC `occurred_at`。更新使用单条 `UPDATE ... jsonb_set(...)` 同时修改设置、追加审计和推进 `updated_at`；不会只覆盖最后一次操作者，也不会改变 `provider_instrument_id + interval` 的不可变身份。管理 API 响应不暴露 `metadata`，避免把内部审计结构耦合给页面。

ADM-002 同样不新增表或 migration。HTTP 层调用 ING-006 已有的原子创建事务，一个请求仍只写入一行 `ingestion_runs` 和一行 `ingestion_tasks`；操作者写入 `requested_by`，actor type、Request ID、reason 及规范化范围写入 Run `context`。活动范围防重继续使用本节既定的事务级 advisory lock，不在 API 层增加进程内锁或永久唯一索引。

ADM-003 不新增表或 migration。Run 管理查询以 Run 为一行聚合关联 Task 状态计数，并在查询投影中计算 `derived_status` 用于筛选；Application 层仍调用 ING-005 的同一状态表生成最终响应，避免使用可能滞后的 `ingestion_runs.status/count` 缓存。Task 管理查询联表 Subscription、ProviderInstrument、Provider 和 Instrument，一次返回 UUID 与可读身份，不在 API Handler 中追加 N+1 查询。首期数据规模先复用现有主键、状态/认领和 retry 索引，观察实际查询计划后再决定是否增加管理列表专用索引。

## 5. 行情表

### 5.1 market_data.market_bars

K 线属于高增长时序数据，不为每行生成 UUID。主键由交易品种、供应商映射、周期、开盘时间和版本共同构成。

```sql
CREATE TABLE market_data.market_bars (
    instrument_id           uuid NOT NULL,
    provider_instrument_id  uuid NOT NULL,
    interval                varchar(8) NOT NULL,
    open_time               timestamptz NOT NULL,
    revision                integer NOT NULL DEFAULT 1,
    close_time              timestamptz NOT NULL,
    open_price              numeric(38,18) NOT NULL,
    high_price              numeric(38,18) NOT NULL,
    low_price               numeric(38,18) NOT NULL,
    close_price             numeric(38,18) NOT NULL,
    base_volume             numeric(38,18),
    quote_volume            numeric(38,18),
    trade_count             bigint,
    is_closed               boolean NOT NULL,
    is_current              boolean NOT NULL DEFAULT true,
    quality_status          varchar(16) NOT NULL DEFAULT 'unchecked',
    provider_updated_at     timestamptz,
    collected_at            timestamptz NOT NULL DEFAULT now(),
    raw_hash                varchar(64),
    metadata                jsonb NOT NULL DEFAULT '{}',

    PRIMARY KEY (
        instrument_id,
        provider_instrument_id,
        interval,
        open_time,
        revision
    ),
    CONSTRAINT fk_market_bars_instrument
        FOREIGN KEY (instrument_id) REFERENCES core.instruments(id),
    CONSTRAINT fk_market_bars_provider_instrument
        FOREIGN KEY (provider_instrument_id)
        REFERENCES market_data.provider_instruments(id),
    CONSTRAINT ck_market_bars_interval CHECK (interval IN ('1h', '1d')),
    CONSTRAINT ck_market_bars_revision CHECK (revision > 0),
    CONSTRAINT ck_market_bars_time CHECK (close_time > open_time),
    CONSTRAINT ck_market_bars_ohlc CHECK (
        low_price <= open_price
        AND low_price <= close_price
        AND high_price >= open_price
        AND high_price >= close_price
        AND high_price >= low_price
    ),
    CONSTRAINT ck_market_bars_volume CHECK (
        (base_volume IS NULL OR base_volume >= 0)
        AND (quote_volume IS NULL OR quote_volume >= 0)
    ),
    CONSTRAINT ck_market_bars_quality
        CHECK (quality_status IN ('unchecked', 'valid', 'warning', 'invalid'))
);

CREATE UNIQUE INDEX uq_market_bars_current
    ON market_data.market_bars (
        instrument_id, provider_instrument_id, interval, open_time
    )
    WHERE is_current = true;

CREATE INDEX idx_market_bars_query
    ON market_data.market_bars (instrument_id, interval, open_time DESC)
    WHERE is_current = true
      AND quality_status IN ('valid', 'warning');
```

写入修订版本时必须在同一事务中关闭旧版本并插入 `revision + 1`。响应内容没有变化时不创建新版本。

### 5.2 market_data.latest_quotes

该表只保存每个来源的最新快照，不作为逐笔历史表。

```sql
CREATE TABLE market_data.latest_quotes (
    instrument_id           uuid NOT NULL,
    provider_instrument_id  uuid NOT NULL,
    market_time             timestamptz NOT NULL,
    last_price              numeric(38,18) NOT NULL,
    bid_price               numeric(38,18),
    bid_size                numeric(38,18),
    ask_price               numeric(38,18),
    ask_size                numeric(38,18),
    open_24h                numeric(38,18),
    high_24h                numeric(38,18),
    low_24h                 numeric(38,18),
    base_volume_24h         numeric(38,18),
    quote_volume_24h        numeric(38,18),
    quality_status          varchar(16) NOT NULL DEFAULT 'unchecked',
    collected_at            timestamptz NOT NULL DEFAULT now(),
    metadata                jsonb NOT NULL DEFAULT '{}',

    PRIMARY KEY (instrument_id, provider_instrument_id),
    CONSTRAINT fk_latest_quotes_instrument
        FOREIGN KEY (instrument_id) REFERENCES core.instruments(id),
    CONSTRAINT fk_latest_quotes_provider_instrument
        FOREIGN KEY (provider_instrument_id)
        REFERENCES market_data.provider_instruments(id),
    CONSTRAINT ck_latest_quotes_last_price CHECK (last_price >= 0),
    CONSTRAINT ck_latest_quotes_bid_price
        CHECK (bid_price IS NULL OR bid_price >= 0),
    CONSTRAINT ck_latest_quotes_ask_price
        CHECK (ask_price IS NULL OR ask_price >= 0),
    CONSTRAINT ck_latest_quotes_quality
        CHECK (quality_status IN ('unchecked', 'valid', 'warning', 'invalid'))
);
```

更新时只接受更晚的 `market_time`；相同市场时间的修订按明确的供应商更新时间或采集规则处理，禁止旧响应覆盖新快照。

## 6. 调度与采集任务表

这些表对应服务内部的 `scheduler` 和 `ingestion` 模块，负责把长期采集配置转换为可恢复、可重试、可审计的数据获取任务。

```text
collection_subscriptions
        │ Scheduler 判断到期任务
        ▼
ingestion_runs
        │ 拆分批次
        ▼
ingestion_tasks
        │ Worker 获取租约并调用 Provider
        ▼
标准化 → 质量检查 → market_bars/latest_quotes
        │
        ▼
ingestion_checkpoints
```

### 6.1 market_data.ingestion_runs

一次定时增量、手动回填、修复或修订形成一个 Run。`run_key` 用于避免同一调度时点重复创建批次。

SCH-003 将自动调度批次的 key 固定为 `run_type.scheduler.trigger.subscription_id.range_start.range_end`；时间使用 UTC 纳秒精度固定格式。一个 key 对应一个 Subscription、一个 close/revision trigger 和一个 Task 范围。跨实例重复插入依赖现有 `uq_ingestion_runs_key` 串行化，因此无需新增 migration 或额外分布式锁。

Repository 使用 `INSERT ... ON CONFLICT (run_key) DO NOTHING` 后读取既有 Run/Task 核对等价性：完全等价返回幂等未创建；同 key 但类型、调度时间、Subscription、范围或 Task 数不同返回 conflict。Subscription 启用状态重检、Run 插入和 Task 插入位于同一事务；Run 成功而 Task 失败时整体回滚。

```sql
CREATE TABLE market_data.ingestion_runs (
    id                  uuid PRIMARY KEY,
    run_key             varchar(192) NOT NULL,
    run_type            varchar(24) NOT NULL,
    trigger_type        varchar(16) NOT NULL,
    status              varchar(16) NOT NULL DEFAULT 'pending',
    scheduled_at        timestamptz,
    started_at          timestamptz,
    finished_at         timestamptz,
    requested_by        varchar(128),
    task_count          integer NOT NULL DEFAULT 0,
    success_count       integer NOT NULL DEFAULT 0,
    failed_count        integer NOT NULL DEFAULT 0,
    context             jsonb NOT NULL DEFAULT '{}',
    error_summary       jsonb NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_ingestion_runs_key UNIQUE (run_key),
    CONSTRAINT ck_ingestion_runs_type
        CHECK (run_type IN ('incremental', 'backfill', 'repair', 'revision')),
    CONSTRAINT ck_ingestion_runs_trigger
        CHECK (trigger_type IN ('scheduler', 'manual', 'recovery')),
    CONSTRAINT ck_ingestion_runs_status
        CHECK (status IN (
            'pending', 'running', 'partial', 'success', 'failed', 'canceled'
        )),
    CONSTRAINT ck_ingestion_runs_counts
        CHECK (task_count >= 0 AND success_count >= 0 AND failed_count >= 0)
);
```

### 6.2 market_data.ingestion_tasks

Task 是 Worker 真正执行的最小任务，一个任务获取某个订阅在指定时间范围内的数据。首期手动回填一次只创建一个 Run 和一个 Task；供应商分页由 Worker 在该 Task 内部完成，不按分页批量创建 Task。

```sql
CREATE TABLE market_data.ingestion_tasks (
    id                      uuid PRIMARY KEY,
    run_id                  uuid NOT NULL,
    subscription_id         uuid NOT NULL,
    retry_of_task_id        uuid,
    range_start             timestamptz NOT NULL,
    range_end               timestamptz NOT NULL,
    status                  varchar(16) NOT NULL DEFAULT 'pending',
    attempt_count           integer NOT NULL DEFAULT 0,
    max_attempts            integer NOT NULL DEFAULT 5,
    next_attempt_at         timestamptz,
    locked_by               varchar(128),
    locked_until            timestamptz,
    started_at              timestamptz,
    finished_at             timestamptz,
    provider_request_id     varchar(128),
    error_code              varchar(64),
    error_message           text,
    error_details           jsonb NOT NULL DEFAULT '{}',
    canceled_by             varchar(128),
    cancel_reason           text,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_ingestion_tasks_run
        FOREIGN KEY (run_id) REFERENCES market_data.ingestion_runs(id),
    CONSTRAINT fk_ingestion_tasks_subscription
        FOREIGN KEY (subscription_id)
        REFERENCES market_data.collection_subscriptions(id),
    CONSTRAINT fk_ingestion_tasks_retry_of
        FOREIGN KEY (retry_of_task_id)
        REFERENCES market_data.ingestion_tasks(id),
    CONSTRAINT uq_ingestion_task_range
        UNIQUE (run_id, subscription_id, range_start, range_end),
    CONSTRAINT ck_ingestion_tasks_range CHECK (range_end > range_start),
    CONSTRAINT ck_ingestion_tasks_attempts
        CHECK (attempt_count >= 0 AND max_attempts > 0),
    CONSTRAINT ck_ingestion_tasks_status
        CHECK (status IN (
            'pending', 'running', 'retry_wait',
            'success', 'failed', 'canceled'
        ))
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
```

Worker 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 抢占任务并设置租约。Run 内相同订阅和范围不能重复；不同 Run 可以重采相同范围，用于修订和修复，最终由行情版本规则保证幂等。

系统自动重试继续使用原 Task 并增加 `attempt_count`。管理员对终态 `failed` Task 发起手动重试时，创建新的 Run 和 Task，并通过 `retry_of_task_id` 保留与原失败任务的审计关系；原 Task 状态和错误信息不被修改。

同一个失败 Task 同时最多存在一个未结束的手动重试任务，`uq_active_manual_retry` 用于防止重复点击或并发请求创建等价任务。

ADM-004 不新增表或 migration。手工重试事务先锁定原 Task，再验证 `failed` 状态、来源有效性和活动后继，最后插入新的 Run/Task；`uq_active_manual_retry` 仍作为并发唯一性的数据库最终防线。取消事务锁定同一 Task 行，更新取消字段后向父 Run 的 `context.operations` JSON 数组追加内部审计项，两次写入一起提交。管理查询不会公开该审计数组，Task 真值与 Run 查询缓存仍保持职责分离。

ADM-005 同样不新增表、物化状态或 migration。Provider 状态 Repository 以 `task_stats + active_sources` CTE 一次读取 Provider 目录、有效订阅、checkpoint 与 Task 最近成功/失败时间；所有健康状态、scope 和 freshness 均在 Service 层按同一个 `checked_at` 动态计算。Provider 总体状态不是数据库事实，避免配置状态、Task 状态和行情新鲜度形成互相漂移的多套真值。

ING-006 的手动 backfill 防重不新增永久唯一索引。创建事务以规范化的 `subscription_id + range_start + range_end` 获取 transaction-level PostgreSQL advisory lock，再联表检查 `run_type = backfill` 且 Task 为 `pending/running/retry_wait` 的完全相同范围。这样可安全串行化跨实例并发创建，同时允许终态后重采相同范围；若在 Task 表上建立全局部分唯一索引，会错误阻止增量或手动重试任务。advisory lock 只覆盖创建短事务，不延伸到 Provider 调用或 Worker 执行期。

### 6.3 market_data.ingestion_checkpoints

```sql
CREATE TABLE market_data.ingestion_checkpoints (
    subscription_id         uuid PRIMARY KEY,
    last_success_open_time  timestamptz,
    last_closed_open_time   timestamptz,
    last_attempt_at         timestamptz,
    last_success_at         timestamptz,
    consecutive_failures    integer NOT NULL DEFAULT 0,
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_ingestion_checkpoints_subscription
        FOREIGN KEY (subscription_id)
        REFERENCES market_data.collection_subscriptions(id),
    CONSTRAINT ck_ingestion_checkpoints_failures
        CHECK (consecutive_failures >= 0)
);
```

Checkpoint 只用于快速恢复和生成增量计划，不是数据完整性的唯一事实来源。缺口检测必须同时检查实际 `market_bars`。

SCH-004 读取 checkpoint 后会枚举从 Subscription `updated_at` 到 `last_closed_open_time` 的全部期望 K 线，并确认每个 open time 对应的 `market_bars` 都是 `is_current=true`、`is_closed=true`、`quality_status IN ('valid','warning')`；任一缺失都会回退到启用边界重新规划，不能仅凭最大时间、最后一根存在或行数替代完整性事实。checkpoint 后的期望 open time 也与实际行情逐一比较。查询复用现有 `provider_instrument_id + interval + open_time` 主键/当前版本索引与时间范围，不新增 migration。

## 9. 数据质量表

### 9.1 market_data.data_quality_issues

```sql
CREATE TABLE market_data.data_quality_issues (
    id                      uuid PRIMARY KEY,
    instrument_id           uuid NOT NULL,
    provider_instrument_id  uuid,
    interval                varchar(8),
    open_time               timestamptz,
    rule_code               varchar(64) NOT NULL,
    severity                varchar(16) NOT NULL,
    status                  varchar(16) NOT NULL DEFAULT 'open',
    summary                 varchar(512) NOT NULL,
    details                 jsonb NOT NULL DEFAULT '{}',
    detected_at             timestamptz NOT NULL DEFAULT now(),
    resolved_at             timestamptz,
    resolution_note         text,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_quality_issues_instrument
        FOREIGN KEY (instrument_id) REFERENCES core.instruments(id),
    CONSTRAINT fk_quality_issues_provider_instrument
        FOREIGN KEY (provider_instrument_id)
        REFERENCES market_data.provider_instruments(id),
    CONSTRAINT ck_quality_issues_interval
        CHECK (interval IS NULL OR interval IN ('1h', '1d')),
    CONSTRAINT ck_quality_issues_severity
        CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    CONSTRAINT ck_quality_issues_status
        CHECK (status IN ('open', 'acknowledged', 'resolved', 'ignored'))
);

CREATE UNIQUE INDEX uq_open_quality_issue
    ON market_data.data_quality_issues (
        instrument_id,
        provider_instrument_id,
        interval,
        open_time,
        rule_code
    )
    NULLS NOT DISTINCT
    WHERE status IN ('open', 'acknowledged');
```

可空维度中的 `NULL` 表示该维度未指定。PostgreSQL 15 的 `NULLS NOT DISTINCT` 保证等价的开放问题不会因为 NULL 而绕过去重；全局问题与指定 Provider 的问题仍可分别存在。

## 10. 事务边界

- 创建 Run 与其关联 Task 使用同一事务；任一任务创建失败时 Run 不进入 `running`。首期手动回填只创建一个关联 Task。
- Task 成功时，行情写入、checkpoint 推进和 Task 状态更新使用同一事务。
- K 线修订时，关闭旧版本和插入新版本使用同一事务。
- Run 汇总计数可以事务内更新或从 Task 聚合重算，数据库中的 Task 状态是最终事实来源。
- Provider 超时且响应状态未知时不推进 checkpoint；允许安全重试采集，因为行情写入具有幂等和版本约束。
- Worker 最终提交前必须检查任务仍为当前 Worker 持有的 `running` 状态；若任务已取消或租约失效，不得写入行情数据。

## 11. 数据库权限

建议角色：

```text
xr_core_owner
xr_market_data_owner
xr_market_data_runtime
```

`xr_market_data_runtime`：

- 只读 `core.assets`、`core.instruments` 和别名表。
- 读写 `market_data` 业务表。
- 不具备建表、删除 schema、修改用户组合或访问交易凭据的权限。
- 数据库迁移使用 owner 角色，服务运行时不使用 owner。

跨 schema 外键由迁移角色创建，需要对目标核心表拥有 `REFERENCES` 权限。

## 12. 分区与容量演进

第一阶段不对 `market_bars` 分区。当前只有 `1h` 和 `1d`，首批资产有限，提前分区会增加唯一约束、迁移和版本管理复杂度。

满足以下任一条件后重新评估：

- `market_bars` 达到数千万行。
- 索引维护、备份或数据清理耗时不可接受。
- 引入分钟级数据导致增长速度显著上升。
- 查询计划持续出现大范围扫描。

需要分区时优先评估按 `open_time` 月度或季度范围分区。大量历史数据和回测快照可进一步归档为 Parquet，但 PostgreSQL 仍保留主数据、当前状态和数据版本元信息。

## 13. 待补充事项

- 现有 SQLite `assets` 和组合关联向 UUID 模型迁移的完整脚本。
- UUIDv7 Go 实现库和生成失败处理。
- `code` 字符集、最大长度及重命名审批流程的最终规范。
- 租约续期的心跳频率和长任务安全停止策略。
- 市场数据采集管理页面的接口字段、权限模型和前端交互细节。
- 行情数据容量压测和 PostgreSQL 参数基线。
- 生产环境备份、恢复目标和数据保留期限。
- `updated_at` 自动维护采用应用写入还是数据库 trigger。
