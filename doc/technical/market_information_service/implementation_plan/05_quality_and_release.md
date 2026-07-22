# 测试、CI 与发布验收计划

## 1. 持续质量任务

| ID | 状态 | 依赖 | 输出 | 完成条件 |
| --- | --- | --- | --- | --- |
| QA-001 | DONE | ENG-002 | `make fmt/vet/coverage/test-race/check` 基线 | OPS-005 后全量 statement coverage 88.1%，observability 94.1%、application 89.1%、HTTP API 86.2%、ingestion 89.1%、scheduler 88.5%、repository 84.9%，race 通过 |
| QA-002 | TODO | DB-006 | `integration` build tag、隔离 PostgreSQL、迁移/Repository 测试命令 | 默认单测不依赖 Docker；集成库可重复创建和清理 |
| QA-003 | TODO | ADP-001 | Adapter fixture、fake server 和真实 smoke test 约定 | fixture 脱敏；真实测试需显式环境变量启用 |
| QA-004 | TODO | ING-001、SCH-001 | 并发、固定时钟、固定 UUID、故障注入工具 | 租约/取消/重试/事务测试可确定性重复执行 |
| QA-005 | TODO | QA-002 | CI 流水线 | PR 执行格式、vet、tidy 漂移、coverage、race、PG 集成测试和构建 |
| QA-006 | TODO | OPS-004 | Docker 镜像构建与依赖/secret/migration 历史检查 | 主分支构建镜像；不包含凭证、覆盖率文件或本地数据 |

## 2. 覆盖率执行规则

- 全量 Go statement coverage 必须 `>= 80.0%`，CI 低于门槛直接失败。
- domain、application、api、ingestion、scheduler 等核心包分别保持不低于 80%，不得靠简单包稀释总覆盖率。
- 新增或修改的非平凡函数覆盖正常、边界和主要失败路径。
- Repository/migration 使用真实 PostgreSQL 集成测试；Handler 用 `httptest`；Adapter 默认用本地 fake server。
- 并发或任务代码必须通过 `go test -race ./...`。
- 缺陷修复先增加可复现的回归测试。

## 3. CI 顺序

1. 检查 `gofmt`、`go vet`、`go mod tidy` 无漂移。
2. 执行单元测试和全量覆盖率门槛。
3. 执行 race test。
4. 启动临时 PostgreSQL，执行 migration 和集成测试。
5. 构建 `market-info` 二进制。
6. 主分支构建 Docker image，并检查 secret、大文件和被修改的历史 migration。

真实 Provider smoke test 不进入普通 CI，避免密钥泄漏、限流与网络波动。

## 4. M5 发布验收清单

### 数据库与启动

- [ ] 空库 migration、逐版本升级和重复执行检查通过。
- [x] runtime 与 migration 数据库角色权限符合设计；00005 向前 migration 固化最小授权并通过隔离 PostgreSQL 验证。
- [x] Compose 独立空 volume 冷启动、PostgreSQL 重启保留演练资产、SIGTERM 退出码 0 和 ready 恢复通过。
- [x] custom archive+checksum 备份恢复演练后，可用 runtime 查询指定演练资产与 migration 版本。

### 行情查询

- [ ] 最新行情多来源同时返回且不会互相覆盖。
- [ ] K 线强制 Instrument、Provider 和 interval。
- [ ] UTC、decimal 字符串、游标和错误 envelope 与文档一致。

### 采集与调度

- [ ] Bybit/Longbridge 最小真实 smoke test 通过。
- [x] 增量采集、单范围 backfill、自动重试、手动重试和取消通过。
- [x] Worker 崩溃/租约过期后可恢复，旧 Worker 不产生数据污染；SCH-004 已接入每轮周期恢复并通过真实 PostgreSQL 验证。
- [x] Run 状态由 Task 事实汇总，完整状态组合、并发快照冲突重算及 PostgreSQL 查询缓存字段已由 ING-005 验证。
- [x] 单范围 backfill 只创建一个 Run/Task；跨实例并发防重、Task 内 Provider 分页和终态后再次回填已由 ING-006 验证，ADM-002 进一步通过真实 PostgreSQL HTTP 测试验证 202、权限/审计、活动范围 409 和缺少订阅 404。
- [x] 重启后使用经行情验证的 checkpoint 续采，并以当前闭合 `valid/warning` 行情事实识别、合并和补齐缺口（SCH-004）。
- [x] 同一自动调度窗口的重复及跨实例并发扫描只创建一个稳定 Run/Task（SCH-003/004）。
- [x] 加密货币按 7×24 小时、UTC 小时/日切和精确 close/revision delay 计算窗口（SCH-001）。
- [x] 美股日历覆盖 DST、官方休市、提前收市和精确 session 边界；休市不累计延迟（SCH-002）。

### 管理、安全与运维

- [ ] 权限、操作者、reason 和 Request ID 审计信息完整（ADM-001 订阅创建/修改、ADM-002 backfill 与 ADM-004 retry/cancel 已通过真实 PostgreSQL HTTP 契约验证；后续管理写 API 继续沿用同一门禁）。
- [x] API、页面和日志不泄漏 Provider 凭证、连接串或堆栈；OPS-001/002 增加日志与指标脱敏回归。
- [x] Provider 状态、任务积压、失败和数据延迟已有持久事实指标与 Prometheus 告警规则，休市不误报。
- [ ] 文档、配置示例、migration 与实际行为一致。

### 质量门禁

- [ ] `make check`、集成测试与 CI 全部通过。
- [ ] 全量及核心包覆盖率不低于 80%。
- [ ] 工作区无构建产物、覆盖率文件、凭证或临时数据库。
- [ ] 发布候选阶段只包含缺陷修复和文档收口。
