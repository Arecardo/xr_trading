# Provider、采集 Worker 与 Scheduler 实施计划

## 1. Adapter 框架与 Provider

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| ADP-001 | DONE | DOM-002、DOM-003 | 已实现 `MarketDataAdapter`、adapter capability、ProviderInstrumentRef、quote/bar DTO、分页 cursor、契约校验与 ProviderError 分类 | fake adapter 契约通过；来源错配、分页/顺序/范围校验通过；认证/限流/网络/无效响应错误可稳定分类；新包覆盖率 95.7% |
| ADP-002 | DONE | ADP-001 | 已实现不可变 AdapterRegistry、启动时 capability 快照、精确市场/类型/操作/interval/limit 校验和稳定错误分类 | 重复注册、未找到、不支持 interval、快照隔离与并发读取测试通过；Registry 包覆盖率 97.2% |
| ADP-003 | DONE | ADP-001、ADP-002 | 已实现 Bybit V5 Spot 公共行情 Adapter：latest、1h、1d、倒序转升序、opaque cursor、限流与错误映射 | 脱敏 `httptest` fixture 覆盖请求/字段/时间/分页/错误/响应上限；真实 smoke test 仅由 `BYBIT_SMOKE=1` 手动启用；包覆盖率 95.4% |
| ADP-004 | DONE | ADP-001、ADP-002 | 已实现 Longbridge 官方 Go SDK 美股/ETF Adapter：latest、常规时段 1h/1d、无复权、倒序分页、opaque cursor、长连接生命周期和结构化错误映射 | 脱敏 fixture 覆盖股票/ETF、DST、常规收盘、分页、错误与敏感信息隔离；真实 smoke test 仅由 `LONGBRIDGE_SMOKE=1` 手动启用；包覆盖率 88.6% |

ADP-003 与 ADP-004 已分别通过统一端口接入，均未把 Provider 特殊字段泄漏到 Worker。ING-001～ING-006 已完成 Worker、K 线采集、自动重试、取消/租约恢复、Run 状态汇总和单任务 backfill 闭环，下一步进入 SCH-001 时间窗口计算器。

## 2. Worker 与任务生命周期

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| ING-001 | DONE | DB-013、ADP-002 | 已实现 Worker claim loop、固定租约参数、claim 契约校验、空队列轮询、认领错误退避、错误报告和严格的进程内并发限制 | 两个 Worker 共享队列无重复执行；单 Worker 不超过配置并发；DB-013 PostgreSQL 并发认领保证进入 `running` 即增加 attempt；取消可中断等待和执行器；新包覆盖率 95.0% 且 race 通过 |
| ING-002 | DONE | ING-001、DB-012、DB-014 | 已实现 K 线 IngestionService：上下文加载、Registry 选择、事务外 Provider 分页、跨页去重/排序、结构质量校验、稳定 raw hash，以及行情修订/质量问题/checkpoint/Task success 最终原子事务 | 应用层包覆盖率 85.4%；故障注入证明 Provider 调用先于最终事务且任一步失败均不提交；最终事务校验 `running + attempt_count + locked_by + locked_until` fencing token 和订阅来源；真实 PostgreSQL 从 claim 到 K 线/checkpoint/success 集成测试通过 |
| ING-003 | DONE | ING-002 | 已实现稳定错误分类、固定分段退避、Provider Retry-After 上限、最大尝试次数、脱敏错误持久化，以及带 fencing 的失败/checkpoint 原子事务 | 网络/限流/临时基础组件错误进入 retry_wait；密钥/映射/契约错误直接 failed；达到上限终止；取消/超时/租约丢失不误转换；单元故障注入和真实 PostgreSQL retry_wait→再次认领→failed 测试通过 |
| ING-004 | DONE | ING-002 | 已实现同任务行锁上的协作式取消、终态 conflict、过期租约原子恢复、`lease_expired` 失败 checkpoint、恢复最大尝试次数和多恢复者 SKIP LOCKED 互斥 | 真实 PostgreSQL 验证运行中取消/租约恢复后旧 Worker 零行情与成功 checkpoint 污染；恢复 Task 可由下一 attempt 接管；达到上限直接 failed；两个恢复者不重复处理 |
| ING-005 | DONE | ING-002 | 已实现独立 RunService、六种 Task 状态快照、完整 Run 状态归约、查询加速字段回写及并发快照冲突重算；采集成功/失败后自动刷新，取消、恢复和详情查询复用同一入口 | 全部 Run 状态及活动/终态混合组合已表驱动覆盖；真实 PostgreSQL 验证状态、计数、开始/结束时间；Task 始终为最终事实 |
| ING-006 | DONE | ING-001～005 | 已实现显式单范围 BackfillService：精确解析 Provider/Instrument/interval、校验历史时间范围与审计字段、解析启用 Subscription、原子创建一个 manual backfill Run/Task；同一 Task 复用现有 Worker 完成 Adapter 分页 | PostgreSQL advisory lock 串行化完全相同的 subscription/range；并发请求一个成功、一个 `ErrBackfillAlreadyRunning`（ADM-002 映射 409）；终态后允许再次创建；真实 PostgreSQL 两页执行、行情提交与 Run success 通过 |

关键 Task 事务必须同时处理行情或修订、质量问题、checkpoint 和 Task 状态。Run 是可由 Task 重建的查询缓存，Task 事务提交后由 Service 单独刷新；刷新失败不得回滚或反向改写已经完成的 Task，后续状态转换和 Run 详情查询会再次校正。故障注入要分别证明 Task 事务不会形成部分提交、旧 Run 快照不会覆盖较新的 Task 事实。

## 3. Scheduler 与市场时间

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| SCH-001 | TODO | DOM-003 | 纯时间窗口计算器、Clock 接口、close/revision delay | Bybit 7×24、小时/日切、边界与重启窗口表驱动测试 |
| SCH-002 | TODO | SCH-001 | 美股常规交易日历接口与 freshness 计算 | 周末、节假日、提前休市、DST；休市返回 not_applicable 且不累计延迟 |
| SCH-003 | TODO | DB-011、DB-013、SCH-001 | 增量 Scheduler、稳定 run_key、Run/Task 原子创建 | 同一调度时点重复运行不重复建任务；Scheduler 不调用 Adapter Fetch |
| SCH-004 | TODO | SCH-003 | checkpoint 续采、缺口检测与过期租约恢复调度 | checkpoint 仅作加速；行情表是完整性事实；重启后安全补齐 |

SCH-001/002 可以与 Worker 并行开发；SCH-003/004 必须等待任务 Repository 和事务契约冻结。

## 4. M3 退出门禁

- Bybit 和 Longbridge 均完成一个受控真实 smoke test，但普通测试无外网依赖。
- 单个增量 Task 从领取到行情/checkpoint 成功提交形成完整闭环。
- 并发、租约、取消、重试、幂等和事务回滚测试通过。
- 美股只按常规交易时段调度；加密按 7×24 调度。
- `go test -race ./...` 与 PostgreSQL 并发集成测试通过。
