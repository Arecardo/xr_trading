# 数据库技术决策

> 状态：已接受  
> 日期：2026-07-05  
> 对应任务：DB-001、DB-002、DB-003

## 1. DB-001：数据库技术栈

- PostgreSQL 最低版本为 15，本地、CI 与生产保持相同大版本。
- 运行时驱动与连接池使用 `pgx/v5` 和 `pgxpool`，Repository 不向 domain 暴露 pgx 类型。
- migration 使用 `pressly/goose/v3` 的 SQL migration，并编译进服务二进制。只有显式 migration 命令可执行 DDL；`serve` 与 `worker` 只检查版本，不自动升级。
- migration 采用连续编号、事务执行和只向前追加规则。进入共享环境后不得修改已发布 migration。
- 精确小数使用 `shopspring/decimal`，数据库使用 `numeric(38,18)`，HTTP API 使用十进制字符串。
- UUIDv7 使用 `google/uuid`，由应用生成；生成失败直接返回错误，不回退 UUIDv4 或零值。
- `updated_at` 首期由应用写入，不使用数据库 trigger。

依赖锁定在 `market-info-service/go.mod`。选择 goose 是因为它同时提供 SQL-only migration、嵌入式文件系统和 Go API，适合提供独立 migration 子命令；不让业务进程启动时自动迁移，是为了保持部署权限和运行权限分离。

## 2. DB-002：Schema 所有权

`core` 是共享核心资产目录，不属于市场资讯服务：

- `xr_core_owner` 拥有 `core` schema 和其中的表。
- `xr_market_data_owner` 拥有 `market_data` schema、表和 migration 版本表。
- 部署使用独立 migrator 身份并切换到 `xr_market_data_owner`；它仅获得创建跨 schema 外键所需的 `USAGE` 与 `REFERENCES`。
- `xr_market_data_runtime` 对 `core` 四张目录表只读，对 `market_data` 业务表按需读写，不授予 DDL、`DELETE`、`TRUNCATE` 或切换 owner 的能力。
- 撤销 `PUBLIC` 对两个业务 schema 的 `CREATE`，并用 owner 的 default privileges 管理未来对象授权。

市场资讯服务的生产 migration 不创建或修改 `core`。本地与 CI 通过独立 bootstrap fixture 创建最小 `core` 前置结构；它不进入市场资讯服务的生产 migration 历史。`core` 缺失时 migration 应明确失败。

跨 schema 外键会形成 DDL 依赖；核心目录变更必须先检查市场资讯服务依赖。如果未来拆为独立数据库，再通过事件同步或本地目录副本替代外键，首期不提前引入这层复杂度。

## 3. DB-003：质量问题幂等键

最低版本已经是 PostgreSQL 15，因此使用原生 `NULLS NOT DISTINCT`：

```sql
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

这使可空维度中的 `NULL` 也按相等处理，避免同一个开放问题被重复创建；问题转为 `resolved` 或 `ignored` 后仍可再次创建新的开放记录。这里的 `NULL` 表示该维度未指定，不表示与所有具体值互斥，因此全局问题和 provider-specific 问题可以同时存在。

集成测试必须覆盖全 NULL、部分 NULL、全部非 NULL、问题解决后重开，以及并发插入。Repository 通过 SQLSTATE `23505` 和索引名映射“开放问题已存在”，不得匹配数据库错误文本。
