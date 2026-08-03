# 市场资讯服务运维、安全与可观测性

> 来源：拆分自 `../10_market_information_service.md` 与 `../11_market_information_database.md`。

## 11. 基础设施策略

当前阶段不建设独立的“基础后台管理平台”。基础设施通过部署配置、迁移、健康检查、日志和运维文档统一管理。

建议初始部署包含：

```text
Docker Compose
  PostgreSQL
  现有业务后端
  market-info-service
```

统一工程约定至少包括：

- Compose 部署配置。
- 环境变量和非敏感配置模板。
- 数据库迁移与版本管理。
- 服务健康检查和 readiness 检查。
- JSON 结构化日志与统一关联 ID。
- 数据库备份和恢复流程。
- 服务启动、停止和故障排查说明。
- 凭据不进入代码库，行情凭据与交易凭据隔离。

### 11.1 暂不引入的组件

| 组件 | 当前决定 | 未来引入条件 |
| --- | --- | --- |
| Redis | 暂不引入 | 热点缓存、低延迟推送或 PostgreSQL 任务协调成为明确瓶颈 |
| Kafka | 暂不引入 | 多个独立消费者、事件重放、消费进度和较高吞吐成为必要能力 |
| Nacos | 暂不引入 | 服务数量增长并明确需要动态配置或服务发现 |
| etcd | 业务不自建 | 由 Kubernetes 等基础设施内部使用，不作为普通业务配置库 |

第一阶段可使用 PostgreSQL 任务表、唯一约束、事务和租约实现任务幂等与多实例协调。后续出现明确问题后再替换，不提前扩张基础设施。

### 11.2 进程模式与生命周期

`market-info` 二进制提供三种互斥运行模式：

| 模式 | HTTP/API | Scheduler | Worker/Adapter | 使用场景 |
| --- | --- | --- | --- | --- |
| `serve` | 是 | 否 | 否 | 独立查询与管理 API；不需要 Provider 凭据 |
| `worker` | 否 | 是 | 是 | 独立后台采集进程；不需要管理 API Bearer 凭据 |
| `all` | 是 | 是 | 是 | 首期单实例或本地一体化运行 |

`worker/all` 必须通过 `MARKET_INFO_ENABLED_PROVIDERS` 显式选择 `bybit`、`longbridge` 或两者。Bybit Adapter 使用公开 Spot API；Longbridge 只有被选择时才从官方 SDK 环境配置读取外部 secret、建立 QuoteContext，并在进程退出时关闭。Scheduler 与 Worker 共享数据库连接池，但只有 Worker 的 IngestionService 持有并复用不可变 Adapter Registry；Scheduler 只计算窗口并创建持久 Run/Task。

Scheduler 启动后立即执行一次，随后按 `MARKET_INFO_SCHEDULER_INTERVAL` 周期扫描。单轮失败记录固定、脱敏的错误码并在下一周期重试，不会停止仍在消费已落库任务的 Worker。任一长运行组件意外结束或返回错误时，统一组件宿主取消其他组件并等待清理；SIGINT/SIGTERM 取消是正常退出。Worker 实例身份可由 `MARKET_INFO_WORKER_ID` 显式指定，未设置时生成 `market-info-worker-<UUID>`，避免多实例共享租约持有者标识。

## 12. 安全与凭据

- Bybit 第一阶段只调用公开 Spot 市场接口，不配置 API Key；Longbridge 使用官方 SDK，从 secret 注入 `LONGBRIDGE_APP_KEY`、`LONGBRIDGE_APP_SECRET`、`LONGBRIDGE_ACCESS_TOKEN` 或 OAuth 配置，且只授予行情权限。
- Longbridge SDK 的 QuoteContext 是持久长连接，由服务启动阶段创建并在退出阶段关闭；不得按 Task 重建，也不得把 SDK 业务 message 或协议帧写入任务错误。
- 如果未来启用 Bybit 私有接口，凭据必须与交易服务隔离并且不具备下单或资金权限。
- 行情服务不得持有下单权限。
- 研究、模拟和实盘交易凭据不得混用。
- 密钥通过环境变量或外部 secret 文件注入，不写入数据库或日志；版本库中的 `.env.example` 只能包含明确的本地占位值，部署时必须替换。
- 原始供应商响应和错误日志必须过滤 token、签名和账户敏感字段。

ADM-001 首期管理 API 使用可替换 `Authenticator` 的静态 Bearer 实现。`serve/all` 模式启动必须提供 `MARKET_INFO_ADMIN_BEARER_TOKEN`，可用 `MARKET_INFO_ADMIN_SUBJECT` 设置审计主体（默认 `market-info-admin`）；纯 `worker` 模式不读取该管理凭据。静态实现构造后只保留 token 的 SHA-256 摘要。该首期凭据拥有 `operations.read`、`subscriptions.manage` 和 `ingestion.manage`，适用于小规模部署，后续可替换为网关/JWT/OIDC 而不改变 Handler 与 Application 契约。公共行情、`/healthz` 和 `/readyz` 仍不鉴权。

具体凭据轮换、加密存储和生产环境 secret 管理方案留待部署设计阶段确定。

## 13. 可观测性

OPS-001 已实现 JSON 结构化日志：HTTP 请求由最外层 Request ID 与内层访问日志/panic recovery 中间件串联，访问日志只记录 method、路由模板、status、耗时和响应字节数，不记录原始 URL/query、headers、body 或原始错误。正常 404 使用 INFO，其他 4xx 使用 WARN，5xx 与 recovered panic 使用 ERROR；未提交响应的 panic 统一返回带相同 Request ID 的 `INTERNAL_ERROR`。

日志上下文支持按执行层级合并 `request_id`、`run_id`、`task_id`、`provider`、`instrument_id` 和 `instrument_code`。backfill、retry、cancel 成功事件使用同一 Request ID 写入新 Run/Task 身份。底层 redacting handler 会继续过滤 token、secret、credential、password、signature、Authorization、Cookie、数据库连接串和原始 error/cause 属性，但调用方仍不得把 `err.Error()`、供应商响应或用户 reason 当作日志 message。

OPS-002 已实现 `GET /metrics`。API counter/histogram 只使用 method、路由模板和状态类；Task 状态/积压与 Provider 健康、连续失败、最后成功、活跃/延迟订阅和数据延迟在 scrape 时从持久事实计算。数据库故障时 endpoint 保持可抓取并将 readiness/snapshot 状态置 0，不返回数据库错误。UUID、symbol、用户、URL、游标和错误全文禁止成为 label。

首期 Prometheus 告警规则位于 `market-info-service/deploy/prometheus/market-info-alerts.yml`：ready 持续失败、连续失败达到 3 次、数据延迟达到 3 个 interval、Task 数量/年龄积压。休市 scope 不输出 delay sample，所以周末和休市不触发延迟告警。Grafana 和集中日志系统仍可在持续运行阶段按需接入，不是当前部署前置条件。

第一阶段先实现：

- 结构化日志。
- 健康检查。
- 采集成功率和失败次数。
- 每个数据源最后成功采集时间。
- 数据延迟与缺口数量。
- API 请求次数和耗时。

Prometheus 应从内部网络抓取 `/metrics`，不得把该运维端点直接暴露到公网。

`/healthz` 与 `/readyz` 首期直接在市场资讯服务内实现。待多个 Go 服务的运行规范稳定后，可将健康检查路由、超时控制、响应结构和数据库 migration 兼容性检查沉淀为 Go 服务底层库，而不在第一阶段提前建设独立基础平台。

## 10. 事务边界

- 创建 Run 与其关联 Task 使用同一事务；任一任务创建失败时 Run 不进入 `running`。首期手动回填只创建一个关联 Task。
- 完全相同活动 backfill 的并发创建使用 transaction-level advisory lock 串行检查；锁只覆盖 Run/Task 创建短事务，终态后允许同范围再次创建。
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

`00005_grant_runtime_permissions.sql` 将 runtime 的 `market_data` 权限固化为 USAGE 与 SELECT/INSERT/UPDATE，并通过 default privileges 覆盖后续表；不授予 DELETE、DDL 或角色管理。Docker Compose 中一次性 migration 容器与长运行 service 使用不同连接串和角色。

## 11.1 容器启动与停止

`market-info-service/Dockerfile` 使用 Go build stage 与非 root Alpine runtime stage。`compose.yaml` 的依赖顺序为 PostgreSQL healthy → migration 成功 → service 启动，healthcheck 指向 `/healthz`，业务依赖状态由 `/readyz` 给出。具体命令见 `market-info-service/README.md`。

普通 `docker compose stop/down` 不删除 named volume；需要保留数据时禁止 `down -v`。应用收到 SIGTERM 后使用配置的 shutdown timeout 等待 HTTP 请求退出，Compose grace period 必须更长。

## 11.2 备份恢复

`scripts/backup.sh` 生成 PostgreSQL custom archive 和 SHA-256；`scripts/restore.sh` 只允许恢复至不同于源库、尚不存在的新数据库，并验证角色、migration、runtime 读取权限及可选 asset code。失败恢复只删除本轮新建目标库，不修改源库。

archive 不含 cluster roles；全新 PostgreSQL 实例先执行 `deploy/postgres/init/001_roles_and_core.sql` 或等价基础设施代码。备份包含目录、行情、任务和审计数据，应加密保存并进行访问控制。首期不实现远端对象存储、自动保留期或跨地域复制。

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
- 管理页面组件、轮询频率和前端交互细节。
- 行情数据容量压测和 PostgreSQL 参数基线。
- 生产环境备份、恢复目标和数据保留期限。
- `updated_at` 自动维护采用应用写入还是数据库 trigger。

## 14. 交易日历维护

- 首期内置 NYSE 2026～2028 官方核心交易日历，并使用 `America/New_York` 处理 DST。
- 日历范围外采取 fail closed；不得自动把周一到周五视为开市。进入新支持年份前必须依据 NYSE 官方页面补充完整休市和提前收市日期，并执行 markettime、scheduler 和 Longbridge 测试。
- 临时全市场休市或临时提前收市通过 `SessionOverride` 注入；更新后需要重算尚未执行的调度窗口，已落库的错误行情通过 repair/revision 流程处理。
- `ErrCalendarOutOfRange` 是配置/运维故障，不是正常休市；ADM-005 和后续告警不得将其降级为 `not_applicable`。

## 15. Scheduler 多实例运行

- 每个实例可以按相同周期调用 `IncrementalScheduler.RunOnce`，不需要 leader election；数据库唯一 `run_key` 是最终幂等边界。
- 等价重复返回 existing，属于正常竞争结果；同 key 非等价返回 conflict，必须记录错误并告警。
- 每个 Run/Task 使用短事务创建，Scheduler 不在事务中调用 Provider，也不跨整个订阅扫描持有数据库事务。
- SCH-004 起每轮先恢复过期租约，再扫描订阅和缺口；恢复失败时停止本轮，防止在任务状态不确定时继续扩张队列。
- checkpoint 前缀的全部期望 K 线必须由当前闭合有效行情验证后才能作为续采位置；失真时回退订阅启用边界。单个 Task 默认最多聚合 500 根连续缺失 K 线，单订阅每轮最多创建 20 个缺口范围，长积压通过后续周期追赶。
- 应监控 `recovered_tasks`、`created_runs`、`existing_runs`、单轮耗时和达到 catch-up 上限的订阅；Run 缓存可能在批量租约恢复后短暂滞后，Task 始终是状态事实。
