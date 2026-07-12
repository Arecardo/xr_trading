# 管理 API、前端与可观测性实施计划

## 1. 管理 API

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| ADM-001 | TODO | DB-011、API-002 | 订阅列表、创建和修改 API | 唯一性、capability、不可变身份、启停语义；不支持 delete/backfill_from |
| ADM-002 | TODO | ING-006、API-002 | 单范围 backfill API | 单 Instrument/Provider/interval/range；202；重复活动范围 409 |
| ADM-003 | TODO | DB-013、ING-005 | Run/Task 列表与详情 | 状态、来源、范围和时间筛选；游标；错误详情脱敏 |
| ADM-004 | TODO | ING-003～005、API-002 | retry/cancel API | failed 手动重试创建新 Run/Task；活动重试唯一；各状态取消冲突正确 |
| ADM-005 | TODO | SCH-002、DB-013 | Provider 状态聚合 API | configured 与 health 分离；美股休市 not_applicable；请求不调用 Provider |

所有写 API 必须记录操作者、reason 和 Request ID，且不得直接修改行情记录。

## 2. 前端纵向切片

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| UI-001 | TODO | ADM-005 契约 | Provider 状态面板 | healthy/degraded/unhealthy/unknown/closed 展示与空态测试 |
| UI-002 | TODO | ADM-001 契约 | 订阅查询、创建、启停页面 | 表单校验、冲突错误、本地化 error.code、无 delete/backfill_from |
| UI-003 | TODO | ADM-003 契约 | Run/Task 列表与详情 | 筛选、分页、状态刷新、脱敏错误展示 |
| UI-004 | TODO | ADM-002/004 契约 | backfill、retry、cancel 操作 | 二次确认、重复点击防护、202 后跳转追踪任务 |
| UI-005 | TODO | QRY-001～003 契约 | Asset→Instrument→Provider→Interval 联动 | 必须展示默认值并显式发送；bars 不依赖后端隐式默认来源 |

前端可以在 API 契约冻结后使用 mock 并行开发；统一 API client 与错误映射文件由唯一负责人维护。

## 3. 可观测性与运维

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| OPS-001 | TODO | ENG-005 | JSON 日志及 request/run/task/provider/instrument 关联字段 | 日志脱敏和字段传播测试；正常 404 不记 ERROR |
| OPS-002 | TODO | QRY-004、ING-005 | Prometheus `/metrics`：API、任务、Provider、延迟与积压 | 指标抓取测试；UUID、symbol、错误全文不得作标签 |
| OPS-003 | TODO | ADM-005、OPS-002 | 首期告警建议：连续失败、数据延迟、任务积压、ready 失败 | 使用固定时间验证阈值；休市不误报 |
| OPS-004 | TODO | DB-006、ENG-004 | Dockerfile、Compose 服务、操作说明 | 冷启动 health=200；迁移后 ready=200；重启保留数据；优雅关闭 |
| OPS-005 | TODO | DB-005 | 最小备份恢复与数据库角色说明 | 空环境恢复演练可查询 seed 数据 |

## 4. M4 退出门禁

- 管理员可完成订阅、单任务回填、重试、取消并追踪状态。
- 普通研究用户只能读取授权状态，不能执行管理写操作。
- Provider 状态不因美股休市降级，不在查询期间探测外部 Provider。
- 管理页面不展示 token、secret、堆栈或数据库信息。
- 日志、指标和页面可通过 Request ID、Run ID、Task ID 串联一次操作。

