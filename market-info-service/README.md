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
