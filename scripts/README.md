# 本地项目服务

使用 [`dev-services.sh`](./dev-services.sh) 统一管理 XR-Trading 本地开发进程：

- PostgreSQL Docker Compose 容器；
- Market Information Go 服务（默认 `8090`）；
- XR-Trading Python Web/BFF（默认 `8080`）。

## 常用命令

```bash
./scripts/dev-services.sh status
./scripts/dev-services.sh start
./scripts/dev-services.sh logs
./scripts/dev-services.sh stop
```

`start` 在 Docker 不可用时会自动以 2 CPU、4 GiB 内存启动 Colima，然后启动 PostgreSQL、执行 migration、构建并启动 Go 服务，最后启动 Web。

`stop` 会停止两个应用进程和 PostgreSQL 容器，但默认保留 Colima，避免影响其他 Docker 项目。如果确认 Colima 只供当前项目使用，可以彻底释放虚拟机资源：

```bash
./scripts/dev-services.sh stop --with-colima
```

脚本只会向自己记录且命令行身份匹配的 PID 发送信号；如果 `8080` 或 `8090` 被其他进程占用，脚本会报告“未纳管”并拒绝误杀。

## 本地配置

配置按以下优先级读取：

1. 当前 shell 环境变量；
2. 仓库根目录 `.env.local`；
3. `market-info-service/.env.local`；
4. `market-info-service/.env.example`。

两个 `.env.local` 文件均已加入 `.gitignore`。例如，可以在根目录 `.env.local` 中配置订阅管理员：

```dotenv
XR_TRADING_SUBSCRIPTION_MANAGERS=你的用户名或邮箱
# 可选；未设置时采集操作权限复用上面的订阅管理员名单
XR_TRADING_INGESTION_MANAGERS=你的用户名或邮箱
```

运行日志和 PID 写入 `.run/dev-services/`，该目录不会提交到 Git。查看最近日志：

```bash
./scripts/dev-services.sh logs
```
