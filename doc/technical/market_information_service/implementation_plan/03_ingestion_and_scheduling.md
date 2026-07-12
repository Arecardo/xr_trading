# Provider、采集 Worker 与 Scheduler 实施计划

## 1. Adapter 框架与 Provider

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| ADP-001 | TODO | DOM-002、DOM-003 | `MarketDataAdapter`、capability、ProviderInstrumentRef、请求/响应 DTO、错误分类 | fake adapter 契约；认证/限流/网络/数据错误可稳定分类 |
| ADP-002 | TODO | ADP-001 | AdapterRegistry 与 capability 校验 | 重复注册、未找到、不支持 interval、并发读取测试 |
| ADP-003 | TODO | ADP-001 | Bybit Spot Adapter：latest、1h、1d、分页与限流 | `httptest` fixture；映射、时间顺序、分页、错误分类；真实 smoke test 手动启用 |
| ADP-004 | TODO | ADP-001 | Longbridge 美股/ETF Adapter：latest、1h、1d | 脱敏 fixture；常规时段映射、错误分类；真实 smoke test 手动启用 |

ADP-003 与 ADP-004 可以由不同负责人并行，均不得把 Provider 特殊字段泄漏到 Worker。

## 2. Worker 与任务生命周期

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| ING-001 | TODO | DB-013、ADP-002 | Worker claim loop、租约与并发限制 | 多 Worker 不重复领取；进入 running 即增加 attempt；取消可退出 |
| ING-002 | TODO | ING-001、DB-012、DB-014 | IngestionService：拉取、标准化、质量校验与最终事务 | 外部调用不持有长事务；最终事务校验 `running+locked_by+lease` |
| ING-003 | TODO | ING-002 | 自动重试、指数退避、最大次数和不可重试错误 | 网络/限流进入 retry_wait；密钥/映射错误直接 failed；达到上限终止 |
| ING-004 | TODO | ING-002 | 协作式取消与过期租约恢复 | 取消或失去租约后零行情/checkpoint 写入；旧 Worker 返回不污染数据 |
| ING-005 | TODO | ING-002 | Run 状态 Service 汇总 | pending/running/success/partial/failed/canceled 全组合测试，Task 为最终事实 |
| ING-006 | TODO | ING-001～005 | 单任务 backfill 执行 | 一个 Run/Task，Adapter 分页在任务内完成；重复活动范围 409 |

关键事务必须同时处理行情或修订、质量问题、checkpoint、Task 状态和 Run 汇总。故障注入要证明任何一步失败都不会形成部分提交。

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

