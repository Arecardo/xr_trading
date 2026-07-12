# 市场资讯服务 Go 开发规范

> 适用范围：`market-info-service` 及后续从中沉淀出的 Go 公共库。  
> 规范状态：第一版，随实现持续修订。  
> 最近更新：2026-07-02

## 1. 目标与基本原则

- 代码必须优先保证领域边界、正确性、可测试性和可观测性，再考虑抽象复用。
- 第一阶段保持单一部署单元和内部模块化，不为尚未出现的复用场景提前拆分微服务或公共平台。
- 依赖方向必须从入口和基础设施指向应用与领域接口，领域层不得依赖 HTTP、数据库驱动或具体 Provider SDK。
- 外部网络、时间、UUID、数据库和 Provider Adapter 等非确定性能力必须通过接口或函数参数注入，以便单元测试替换。
- 所有时间在服务内部统一使用 UTC；金额、价格和成交量不得使用二进制浮点数表达。

## 2. Go 版本与模块

- 使用当前项目锁定的 Go toolchain，版本写入 `go.mod`，CI 与本地开发保持一致。
- 服务采用独立 Go module，目录为 `market-info-service/`。
- 引入第三方依赖前必须确认维护状态、许可证和必要性；标准库可以清晰完成的功能不额外引入框架。
- 提交必须包含 `go.mod` 与 `go.sum`，禁止依赖未固定版本的本地 replace。

## 3. 目录与依赖方向

```text
market-info-service/
  cmd/
    market-info/          # 进程装配与启动，保持轻薄
  internal/
    api/                  # HTTP 路由、请求解析、响应编码
    application/          # 用例编排与事务边界
    domain/               # 领域对象、值对象和领域接口
    ingestion/            # 采集任务执行
    scheduler/            # 调度与任务生成
    providers/            # Provider Adapter 实现
    repository/           # PostgreSQL Repository 实现
    config/               # 配置加载与校验
    observability/        # 日志、指标、健康检查
  migrations/             # 只向前执行的数据库 migration
  testdata/               # 测试固定数据，不包含凭证
```

依赖约束：

- `cmd` 负责依赖装配，不承载业务规则。
- `api` 只能调用 application use case，不直接调用 SQL 或 Provider SDK。
- `application` 依赖 domain 定义的接口，负责权限之外的业务校验、事务和流程编排。
- `domain` 不依赖其他内部基础设施包。
- `repository` 与 `providers` 实现 domain/application 所需接口，不反向定义业务规则。
- Go 的 `internal` 机制用于阻止其他服务直接依赖内部实现。

## 4. 命名与代码风格

- 所有代码必须通过 `gofmt`；提交前执行 `go fmt ./...`。
- 包名使用简短小写单词，不使用 `util`、`common`、`base` 等含义模糊的包名。
- 接口由使用方定义，名称描述能力，例如 `QuoteRepository`、`MarketDataAdapter`。
- 导出标识符必须有符合 GoDoc 约定的注释；仅为测试而使用的对象不随意导出。
- 函数保持单一职责；出现多层条件、多个外部副作用或难以独立测试时，应拆分为可命名的步骤。
- 不设置武断的函数行数上限，但代码评审必须能清楚说明输入、输出、副作用和失败路径。
- 禁止使用 `panic` 表达可预期业务错误；`panic` 只用于无法继续的编程不变量，并由进程边界统一恢复和记录。

## 5. Context、并发与资源管理

- 所有可能阻塞的 I/O 和 application use case 均以 `context.Context` 作为第一个参数。
- 不把 Context 保存到结构体，不传递 `nil` Context。
- HTTP、数据库和 Provider 请求必须设置超时；超时来自配置，并设置合理默认值与上限。
- 启动的 goroutine 必须有明确的所有者、退出条件和等待机制，禁止无法停止的后台 goroutine。
- 共享状态优先通过数据库约束和事务保证；进程内锁只能保护单进程内存，不能代替任务租约。
- Channel 由生产方关闭，消费方不得关闭不属于自己的 Channel。

## 6. 错误处理

- 底层错误使用 `%w` 包装并补充操作上下文，例如 `fmt.Errorf("query latest quote: %w", err)`。
- 业务错误使用稳定错误码，通过 `errors.Is` / `errors.As` 判断，不根据错误文本分支。
- HTTP 层统一映射业务错误到设计文档规定的状态码与 error envelope。
- 对外错误必须脱敏，不返回 SQL、堆栈、连接串、Provider token、secret 或签名内容。
- 日志只在能够采取动作的边界记录一次，避免同一错误在每层重复输出。
- `retryable` 由明确的错误分类产生，不通过字符串匹配猜测。

## 7. 数据、时间与标识

- 主键使用 UUIDv7；数据库关系和服务内部 Map key 使用 UUID，不使用可变 symbol 充当身份。
- 对外 API 同时保留稳定、可读的 `code`，入口解析后尽早转换为 UUID。
- 时间字段使用 `time.Time`，写库和序列化前统一为 UTC；API 使用 RFC 3339。
- 价格、成交量等 decimal 数据在领域层使用精确十进制类型，在 JSON 中序列化为字符串。
- 行情记录必须保留 `instrument_id`、`provider_instrument_id` 和 `source`，禁止按 Asset 覆盖不同来源。
- 数据库写入依赖唯一约束和事务保证幂等，不以“先查询再插入”作为唯一并发保护。

## 8. HTTP API

- API 路径、状态码、分页和错误结构遵循 `07_api_and_admin_ui.md`。
- Handler 只负责参数解析、基础格式校验、调用 use case 和响应映射。
- 所有响应包含 `X-Request-ID`；日志和手动任务上下文使用同一 Request ID。
- JSON decoder 应拒绝未知字段，避免客户端拼写错误被静默忽略。
- 请求体必须限制大小；列表接口必须限制 page size，并设置安全默认值和最大值。
- `/healthz` 不访问外部依赖；`/readyz` 使用短超时检查 PostgreSQL 与 migration 兼容性。

## 9. 配置与敏感信息

- 配置通过环境变量或显式配置文件注入，不在代码中硬编码环境差异。
- 配置加载后必须集中校验，缺少必填项时在启动阶段快速失败。
- Provider 凭证只从密钥环境变量读取，不进入数据库、日志、错误响应和测试固定数据。
- 配置结构应区分值、默认值和是否显式设置，避免零值产生歧义。
- 示例配置只能使用明显无效的占位值。

## 10. 数据库与 migration

- 使用 PostgreSQL，SQL schema 以版本化 migration 为实施事实来源，设计文档与 migration 同步维护。
- migration 只向前追加；已进入共享环境的 migration 不允许原地修改。
- Repository 方法负责 SQL 与数据库模型转换，不向上泄漏驱动特有类型。
- 多表一致性、任务最终提交和 Run/Task 创建必须使用显式事务。
- 所有 SQL 必须使用参数绑定，禁止拼接用户输入。
- 数据库错误要映射为稳定的领域错误，例如唯一冲突、外键冲突或暂时不可用。

## 11. 日志与可观测性

- 使用结构化日志，至少包含 `request_id`、`run_id`、`task_id`、`provider`、`instrument_id` 等适用字段。
- 禁止记录完整请求头、凭证、签名原文或未经限制的 Provider 响应体。
- INFO 记录生命周期和可操作事件，WARN 记录可恢复异常，ERROR 记录需要处理的失败；正常 404 不作为 ERROR。
- 指标名称、标签和值域必须受控，禁止将 UUID、symbol 或错误全文作为高基数标签。

## 12. 测试规范

### 12.1 单元测试要求

- 每个新增或修改的非平凡函数必须有单元测试覆盖其正常路径、边界条件和主要失败路径。
- 单元测试与实现放在同一 package 或 `_test` package，优先使用表驱动测试和 `t.Run`。
- 测试必须可重复、可并行且不依赖执行顺序；适合并行的测试调用 `t.Parallel()`。
- 单元测试不得访问真实网络、真实 Provider 或开发者数据库；使用接口 fake、`httptest`、固定时钟和 `testdata`。
- 测试断言应验证可观察行为，不绑定无关实现细节。
- 修复缺陷时必须先增加能够复现该缺陷的回归测试。

### 12.2 集成测试

- PostgreSQL Repository、migration 和真实 HTTP 路由使用独立集成测试覆盖。
- 集成测试使用隔离数据库并可重复创建与清理，不复用开发或生产 schema。
- 需要 Docker 或外部环境的测试使用明确的 build tag 或独立命令，不混入默认快速单元测试。
- Provider 契约测试默认使用录制后脱敏的响应或本地 fake server；真实 API 测试必须显式启用。

### 12.3 覆盖率门槛

- 项目 Go 代码行覆盖率以 `go test` 的 statement coverage 为准，最低为 **80%**。
- 覆盖率必须由全量 package 汇总计算，不能只挑选高覆盖率 package。
- 生成代码、纯 SQL migration 和第三方代码不进入 Go 覆盖率；普通启动装配代码不豁免。
- 单个核心 package（domain、application、ingestion、scheduler、api）不得以整体覆盖率达标为由长期缺少测试。
- 覆盖率是最低门槛，不替代对并发、事务、错误分类和边界条件的测试审查。

标准验证命令：

```bash
go test ./... -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out
```

`total` 行低于 `80.0%` 时，CI 必须失败。涉及并发、租约、Scheduler 或 Worker 的修改还必须执行：

```bash
go test -race ./...
```

## 13. 提交前质量门禁

每次提交前至少执行：

```bash
go fmt ./...
go vet ./...
go test ./... -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out
```

代码进入主分支前必须满足：

- 格式化、编译和静态检查通过。
- 单元测试全部通过，代码行覆盖率不低于 80%。
- API、migration 或配置发生变化时，同步更新对应设计文档和示例。
- 不包含凭证、临时数据库、覆盖率输出、构建产物或编辑器文件。
- 新依赖有明确用途，不引入重复能力或不必要的框架。

