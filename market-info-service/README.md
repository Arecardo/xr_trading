# Market Information Service

Go 行情服务的本地运行与最小运维入口。详细领域/API 设计见 [`../doc/technical/market_information_service/`](../doc/technical/market_information_service/)。

## Docker Compose 冷启动

`.env.example` 仅用于本地开发。部署前必须通过 `.env.local` 或部署平台 secret 替换 PostgreSQL 密码和 `MARKET_INFO_ADMIN_BEARER_TOKEN`。

```bash
docker compose --env-file .env.example up -d --build market-info
docker compose --env-file .env.example ps
curl --fail http://127.0.0.1:8090/healthz
curl --fail http://127.0.0.1:8090/readyz
curl --fail http://127.0.0.1:8090/metrics
```

`market-info` 会等待 PostgreSQL healthcheck 和一次性 `migrate` 容器成功。migration 使用 `xr_market_data_owner`，运行服务通过 `SET ROLE xr_market_data_runtime` 使用最小 DML 权限。

普通停止会保留 PostgreSQL named volume：

```bash
docker compose --env-file .env.example stop market-info postgres
```

验证优雅关闭可使用：

```bash
docker compose --env-file .env.example stop --timeout 15 market-info
docker compose --env-file .env.example logs market-info
```

不要在需要保留数据时执行 `docker compose down -v`。

## 备份与空库恢复

备份脚本使用 PostgreSQL custom archive，并同时生成 SHA-256 文件：

```bash
./scripts/backup.sh ./backups/xr-trading.dump
```

恢复只允许写入一个尚不存在且不同于源库的新数据库，不会覆盖原库：

```bash
./scripts/restore.sh ./backups/xr-trading.dump xr_trading_restore
```

若需要在演练中验证某个已知资产，可传入第三个参数：

```bash
./scripts/restore.sh ./backups/xr-trading.dump xr_trading_restore asset.crypto.btc
```

脚本会验证 checksum、三类数据库角色、migration 版本、runtime 对 `core.assets` 和 `market_data.providers` 的读取权限。确认演练结果后，由操作者显式清理演练库：

```bash
docker compose --env-file .env.example exec -T postgres \
  dropdb --username postgres xr_trading_restore
```

custom archive 不包含 cluster role 定义；空 PostgreSQL 实例必须先运行 `deploy/postgres/init/001_roles_and_core.sql`，生产环境则由基础设施代码创建等价角色。备份文件可能包含完整行情和目录数据，应按生产数据加密、限制访问并设置保留期。

## 隔离 PostgreSQL 集成测试

普通单元测试不访问 Docker 或 PostgreSQL：

```bash
make test
```

集成测试使用一次性的 Compose project、随机宿主端口和独立 named volume，自动初始化 `core`、执行最新 migration，并在成功或失败后清理容器和 volume：

```bash
make test-integration
```

每次执行都是空库，不会读写开发库。排查失败时可临时保留环境；完成排查后必须用输出的 project 名显式清理：

```bash
MARKET_INFO_INTEGRATION_KEEP=1 make test-integration
docker compose --project-name <project> --env-file .env.example down --volumes
```

`make test-integration-existing` 只作为已准备好一次性数据库时的逃生入口，需要显式设置 `MARKET_INFO_ALLOW_EXISTING_INTEGRATION=1`，并由调用方提供三个数据库 URL、执行 migration 和负责清理；CI 和日常验收统一使用隔离命令。

## Adapter 测试与真实 smoke

Adapter 默认测试完全离线：Bybit 使用只监听本机随机端口的 `httptest.Server`，Longbridge 在官方 SDK Client 接口处注入 fake。两者均使用 `internal/providers/*/testdata` 中的脱敏 JSON fixture；测试会校验 fixture 是有效 JSON，且不包含凭据字段、Bearer 值或私钥标记。

```bash
make test-adapters
```

真实 Provider smoke test 同时要求 `smoke` build tag 和显式环境变量，普通 `go test ./...` 即使继承了凭据或 smoke 开关也不会访问外网：

```bash
BYBIT_SMOKE=1 make smoke-bybit
LONGBRIDGE_SMOKE=1 make smoke-longbridge
```

Bybit 首期只访问公共 Spot 行情，不读取 API Key；可用 `BYBIT_BASE_URL` 指定官方测试网或区域端点。Longbridge 由官方 SDK 读取 `LONGBRIDGE_*` 配置，必须从本地 secret 或部署平台注入最小只读行情权限。真实 smoke 不进入普通 CI，输出、fixture 和提交内容中不得包含凭据或原始敏感响应。

## 持续集成

GitHub Actions 在 Pull Request、`master` push 和人工触发时运行以下独立门禁：

- `make security-check`，扫描仓库策略和完整 Git 历史；它是其余 job 的前置依赖
- `make fmt-check tidy-check vet`
- `make coverage`，全量 statement coverage 不低于 80%
- `make test-race`
- `make test-integration`，使用自动销毁的隔离 PostgreSQL
- `make build`，构建服务和 migration 两个 Linux 二进制

覆盖率 profile 和构建产物保留 14 天。普通 CI 不读取 Provider 凭据、不执行真实 smoke，也不部署任何环境。开发者提交前可用 `make check` 执行不依赖 Docker 的同等本地门禁；`make fmt` 仅用于主动格式化代码。

本地 `make security-check` 需要先安装 Gitleaks，例如 macOS 使用 `brew install gitleaks`。详细禁止项、网络配置规则、误报审批和泄漏响应见 [安全开发规范](../doc/technical/12_security_development_standards.md)。
