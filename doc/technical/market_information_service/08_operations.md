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

## 12. 安全与凭据

- Bybit 第一阶段只调用公开 Spot 市场接口，不配置 API Key；Longbridge 使用官方 SDK，从 secret 注入 `LONGBRIDGE_APP_KEY`、`LONGBRIDGE_APP_SECRET`、`LONGBRIDGE_ACCESS_TOKEN` 或 OAuth 配置，且只授予行情权限。
- Longbridge SDK 的 QuoteContext 是持久长连接，由服务启动阶段创建并在退出阶段关闭；不得按 Task 重建，也不得把 SDK 业务 message 或协议帧写入任务错误。
- 如果未来启用 Bybit 私有接口，凭据必须与交易服务隔离并且不具备下单或资金权限。
- 行情服务不得持有下单权限。
- 研究、模拟和实盘交易凭据不得混用。
- 密钥通过环境变量或外部 secret 文件注入，不写入数据库、配置样例和日志。
- 原始供应商响应和错误日志必须过滤 token、签名和账户敏感字段。

具体凭据轮换、加密存储和生产环境 secret 管理方案留待部署设计阶段确定。

## 13. 可观测性

第一阶段先实现：

- 结构化日志。
- 健康检查。
- 采集成功率和失败次数。
- 每个数据源最后成功采集时间。
- 数据延迟与缺口数量。
- API 请求次数和耗时。

服务应预留 Prometheus 指标接口。Grafana 和集中日志系统可在持续运行阶段按需要接入，不作为首期阻塞项。

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
