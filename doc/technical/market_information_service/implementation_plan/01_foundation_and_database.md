# 工程基础与数据库实施计划

## 1. 前置决策

| ID | 状态 | 输入 | 输出与完成条件 | 测试/验证 |
| --- | --- | --- | --- | --- |
| DB-001 | DONE | 数据库设计、Go 规范 | 已冻结 goose、pgx/v5、shopspring/decimal、google/uuid 与 PostgreSQL 15，依赖已锁定 | numeric 扫描与真实连接验证纳入 DB-007/DB-009 |
| DB-002 | DONE | 数据所有权设计 | `core` 独立所有；生产 migration 只管理 `market_data`；bootstrap/migration/runtime 角色分离 | 角色集成测试纳入 DB-005/DB-006 |
| DB-003 | DONE | `data_quality_issues` 唯一索引 | 采用 PG15+ `NULLS NOT DISTINCT`，NULL 表示维度未指定 | NULL 与并发去重集成测试纳入 DB-014 |

DB-001～003 完成前不得冻结初始 migration。

## 2. 工程基础任务

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| ENG-001 | DONE | 无 | Go 开发规范 | 文档已索引 |
| ENG-002 | DONE | ENG-001 | `go.mod`、配置包、HTTP Server、health handler | 单测、race、coverage 通过 |
| ENG-004 | READY | DB-001 | `cmd/market-info`、`serve/worker/all` 模式解析、信号与依赖装配 | 启动参数、取消、错误退出单测；二进制可构建 |
| ENG-005 | READY | ENG-002 | 结构化日志、Request ID、panic recovery、统一错误 envelope | middleware 顺序、ID 传播、脱敏、错误映射单测 |
| ENG-006 | TODO | ENG-004、DB-004 | 示例配置和启动 README | 缺必填配置快速失败；示例不含有效凭证 |

## 3. Migration 与本地 PostgreSQL

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| DB-004 | DONE | DB-001～003 | 已实现嵌入式连续 migration、独立 migration 命令、Make target 与版本兼容 readiness checker | 空库全量 up、重复执行、迁移连续性检查、版本不兼容 checker 单测通过 |
| DB-005 | IN_PROGRESS | DB-004 | 已实现 Provider/行情/任务/质量表初始 DDL、独立 `core` bootstrap 与授权脚本；已完成真实 PG 表/索引、代表性 CHECK 和权限边界验收 | 待 Repository 集成测试逐项覆盖非法 FK/CHECK/唯一值；runtime 已验证无 DDL/core 写权限 |
| DB-006 | DONE | DB-004 | 已添加 Compose PostgreSQL 15、healthcheck、`.env.example` 和持久卷；本地 Colima/Docker Compose 冷启动验收通过 | 空卷启动、迁移、重复迁移、重启保留 migration 版本通过 |
| DB-007 | TODO | DB-004、DB-006 | 连接池生命周期、超时、Ping、真实 `/readyz` 装配 | 断连、超时、关闭、schema 不兼容和恢复集成测试；不调用 Provider |
| DB-008 | TODO | DB-005 | 开发 seed：Asset/Instrument/Longbridge/Bybit 示例，不含密钥 | seed 幂等，从空库可查询测试行情 |

初始 DDL 在尚无共享环境时可以是一个版本，但 SQL 内必须按上述依赖章节组织。进入共享环境后只允许新增 migration，不得修改历史文件。

## 4. Repository 基础

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| DB-009 | TODO | DB-001 | 领域数据库值转换、Repository 错误类型、事务接口 | domain 不依赖 pgx；UTC、decimal、UUID、错误映射单测 |
| DB-010 | TODO | DB-005、DB-009 | 主数据与 Provider Repository | 真实 PG 集成测试覆盖查询、默认 Provider 部分唯一约束 |
| DB-011 | TODO | DB-005、DB-009 | 订阅 Repository | 唯一订阅、启停、不可变身份和分页测试 |
| DB-012 | TODO | DB-005、DB-009 | LatestQuote 与 MarketBar Repository | 旧 quote 不覆盖新 quote、多来源不覆盖、K 线版本与分页测试 |
| DB-013 | TODO | DB-005、DB-009 | Run/Task/Checkpoint Repository 与 claim/lease 事务 | `SKIP LOCKED` 不重复领取、attempt、租约、取消和原子提交测试 |
| DB-014 | TODO | DB-005、DB-009 | DataQualityIssue Repository | 开放问题去重、确认/解决状态转换测试 |

DB-010、DB-011、DB-012、DB-014 可在契约冻结后并行；DB-013 由单一负责人实现，因为它集中承载任务并发和事务语义。

## 5. M1 退出门禁

- 从空库可一条命令启动 PostgreSQL 并执行 migration。
- migration 与设计表、约束和索引追踪清单无遗漏。
- `market-info` 二进制可启动和优雅退出。
- PostgreSQL/schema 正常时 `/readyz=200`，断连或不兼容时为 `503`。
- runtime 数据库角色没有 DDL 权限，日志与错误不泄漏连接串。
- `make check` 和数据库集成测试通过，总覆盖率不低于 80%。
