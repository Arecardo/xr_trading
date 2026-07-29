# 测试、CI 与发布验收计划

## 1. 持续质量任务

| ID | 状态 | 依赖 | 输出 | 完成条件 |
| --- | --- | --- | --- | --- |
| QA-001 | DONE | ENG-002 | `make fmt/vet/coverage/test-race/check` 基线 | QA-004 后全量 statement coverage 88.3%，testkit 98.8%、observability 94.1%、application 89.1%、HTTP API 86.2%、ingestion 89.1%、scheduler 88.5%、repository 84.9%，race 通过 |
| QA-002 | DONE | DB-006 | `integration` build tag、隔离 PostgreSQL、迁移/Repository 测试命令 | Colima 停止时默认单测通过；隔离 Compose 空库连续执行两轮 migration 与全部数据库集成测试通过，容器、network 和 volume 均自动清理 |
| QA-003 | DONE | ADP-001 | Adapter fixture、fake server 和真实 smoke test 约定 | 7 个 JSON fixture 有效性/敏感字段门禁及两套离线 Adapter 测试通过；真实测试需要 `smoke` build tag 与 Provider 专属环境开关双重启用 |
| QA-004 | DONE | ING-001、SCH-001 | 并发、固定时钟、固定 UUID、故障注入工具 | 共享 testkit 覆盖率 98.8% 且 race 通过；Scheduler 身份/时间、租约/取消 fencing、重试和五阶段事务回滚可确定性重复执行，隔离 PostgreSQL 套件通过 |
| QA-005 | DONE | QA-002 | GitHub Actions CI 流水线 | 首次 push run 的格式、vet、tidy 漂移、88.3% coverage、race、隔离 PG 集成测试和双二进制构建全部通过；后续增加仓库策略与 Gitleaks 全历史安全前置门禁 |
| QA-006 | TODO | OPS-004、QA-005 | Docker 镜像构建、依赖漏洞、镜像内容和 migration 历史检查 | 主分支构建镜像；镜像不包含凭证、覆盖率文件或本地数据；仓库 secret 扫描已由 QA-005 前置完成 |

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

## 3.1 QA-002 隔离数据库契约

- `make test` 只执行无 `integration` build tag 的测试，不启动或探测 Docker/PostgreSQL。
- `make test-integration` 是 CI 和本地验收的标准入口；每次创建独立 Compose project、随机宿主端口和独立 named volume。
- 脚本先在新库初始化 `core` 前置表，再以 migration 角色执行嵌入式 migration，测试分别使用 runtime 和 admin URL。
- 成功、测试失败或 migration 失败均通过退出 trap 清理整个 Compose project；`MARKET_INFO_INTEGRATION_KEEP=1` 只用于人工排错。
- `make test-integration-existing` 必须显式设置危险操作开关，调用方对一次性数据库、migration 和清理负责，不作为 CI 入口。
- 集成套件统一位于 `internal/database/integration`，使用 `//go:build integration`，从而保证默认单测不会误连数据库。

## 3.2 QA-003 Adapter 测试契约

- Bybit 普通测试只能通过 `httptest.Server` 调用本机 fake HTTP 端点；测试校验方法、路径、query、分页和错误映射，不调用真实 Bybit 域名。
- Longbridge 官方 SDK 的网络层不作为本服务测试边界；普通测试通过注入最小 Client 接口 fake，使用固定 SDK DTO 验证映射、分页、市场时段和错误分类。
- Provider fixture 只保留映射所需的最小字段，不允许提交原始生产响应。统一回归测试校验所有 fixture 是有效 JSON，并递归拒绝 API/App Key、secret、token、Authorization、Cookie、签名、密码、私钥等字段或标记。
- `make test-adapters` 是完全离线的 Adapter 回归入口，属于普通 CI；它不得读取 Provider 凭据。
- 真实测试文件使用 `//go:build smoke`，并由 `BYBIT_SMOKE=1 make smoke-bybit` 或 `LONGBRIDGE_SMOKE=1 make smoke-longbridge` 再次确认。缺少任一层门禁时不得发送请求。
- Bybit smoke 只访问公共 Spot 行情，不使用 API Key；Longbridge smoke 仅接受外部注入的只读行情凭据。真实 smoke 不进入普通 PR CI，fixture、错误和日志不得保存原始敏感响应。
- QA-003 只冻结测试设施和安全门禁，不代表真实供应商验收已经通过；M5 的 Bybit/Longbridge 最小真实 smoke 清单保持未完成，待具备网络和凭据时人工执行。

## 3.3 QA-004 确定性测试工具契约

- `internal/testkit.ManualClock` 提供线程安全的固定 UTC 时间、显式 `Set` 和 `Advance`；业务测试不得依赖墙上时钟推进来触发租约、重试或调度边界。
- `IDSequence` 按给定顺序返回固定 UUID，耗尽时明确失败，避免测试悄悄回退到随机身份；Scheduler 测试同时断言调用次数和 Run/Task 身份。
- `Gate` 通过 context 感知的 entered/release 边界协调 goroutine。取消和旧 Worker fencing 测试必须等待确定事件，不使用 `time.Sleep` 猜测 Provider 调用是否已经开始。
- `FaultPlan` 在命名 checkpoint 按顺序返回错误并记录命中次数。Repository 成功事务在 `begin/bar/checkpoint/task/commit` 任一点失败都必须回滚，且注入点只命中一次。
- Provider 的分类错误继续作为重试故障注入边界；固定完成时间下断言 `retry_wait/failed`、退避时间、最大重试上限和凭据脱敏。
- 并发工具自身和 Worker/Scheduler/Repository 相关包必须通过 `go test -race`；真实 PostgreSQL 套件继续验证数据库锁、租约 fencing 和并发恢复，不能只依赖内存 fake。
- 超时 context 只作为测试死锁的最终保险，不作为 goroutine 正确排序的机制；测试成功路径必须由 Gate、channel 事件或事务结果推进。

## 3.4 QA-005 GitHub Actions 契约

- GitHub Actions 是首期 CI 执行状态、日志和构建产物的事实来源；不为 CI、部署和服务器控制重复建设一套可写运维后台。
- 工作流在 Pull Request、`master` push 和人工触发时执行，使用只读 `contents` 权限，并按 workflow/ref 取消已过时的同分支运行。
- 静态、coverage、race、隔离 PostgreSQL 集成和构建拆成独立 job 并行反馈；每个 job 设置超时，不共享数据库或可变工作区。
- `make fmt-check` 只检查格式而不修改 CI 工作区；`make tidy-check` 执行 `go mod tidy` 后检查 `go.mod/go.sum` 无漂移；本地和 CI 复用相同 Make target。
- coverage profile 与 Linux 双二进制作为短期 artifact 保留 14 天；CI Summary 展示总 statement coverage，低于 80% 直接失败。
- 普通 CI 不配置 Longbridge/Bybit 凭据、不执行真实 Provider smoke、不执行生产部署。部署和镜像供应链门禁属于 QA-006 及后续发布流程。
- 后续如需要统一入口，只增加只读“工程运维概览”：展示最新 CI 结果、覆盖率、commit/image/migration/config 版本、环境健康和原平台链接；首期不保存平台凭据，也不提供部署按钮。
- 测试环境绑定使用不可变关系 `environment → commit SHA → image digest → migration version → config version`，由 CI/部署阶段写入；不得在页面中任意绑定一个可变 URL 来代替发布身份。
- `Security preflight` 是其余五个 job 的强制 `needs` 依赖；它执行仓库策略扫描器单测、敏感文件/IP/端口规则和 Gitleaks 完整历史扫描。安全门禁失败时不启动编译、测试或 artifact 上传。
- Gitleaks 版本与 Linux x64 release SHA-256 固定在工作流中；checkout 使用完整历史且不持久化凭据，扫描日志启用 100% redact，不上传 finding 报告。
- 安全规则的仓库级定义和泄漏响应见 [安全开发规范](../../12_security_development_standards.md)。

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
