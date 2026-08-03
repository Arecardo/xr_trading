# 安全规范（操作性补充与索引）

> 适用范围：整个仓库。本文件是 `doc/technical/12_security_development_standards.md` 的**操作性补充与索引**，不重复其全文。
> 规范状态：第一版。
> 最近更新：2026-08-04

## 1. 权威来源

- 仓库级安全开发规范全文：`doc/technical/12_security_development_standards.md`（禁止入库信息、配置/网络规则、评审流程、自动门禁、泄漏响应）。
- 行情服务运维/安全：`doc/technical/market_information_service/08_operations.md`。
- 本文件只补充 Agent 与交易场景下的操作性红线。

## 2. 绝对红线（任何 Agent 都不得逾越）

1. **不执行真实资金操作**：不下真实订单、不撤资、不转账、不切换 `live`。这些一律交由用户本人。
2. **不将密钥或真实账户数据写入仓库**：包括代码、文档、测试固定数据、日志、截图、错误上下文。
3. **不绕过风控**：任何订单意图必须先过组合级 + 订单级风控。
4. **不绕过安全门禁**：不用宽泛 allowlist、关闭规则或 `gitleaks:allow` 绕过；误报例外须限定单一 finding 并记录原因/负责人/到期。
5. **不盲目重试下单**：API 超时或订单状态未知时进入安全状态、人工介入。

## 3. 禁止进入仓库的信息（摘要）

真实 API Key/App Key/Access Token/Bearer/密码/Cookie/签名/助记词/私钥/证书私钥/DB 连接凭据；非回环 IP、主机名、端口组合、内网域名、云资源 ID；`.env`、credential 文件、密钥库、数据库文件、备份、日志、抓包、Provider 原始响应、覆盖率文件；含真实账户/订单/持仓/资金/个人信息的 fixture/截图/SQL/日志。

允许：`127.0.0.1`/`localhost`/`::1`、Compose 通用服务名、明显 `example`/`local-only` 占位值、Provider 官方公开域名与文档链接（认证信息单独注入）。

## 4. 凭据与配置注入

- 代码只读环境变量或 Secret Manager 引用，不硬编码环境地址/端口/凭据。
- `.env.example` 只列变量名与不可用占位；真实 `.env` 由 `.gitignore` 排除。
- 缺关键凭据时启动失败（fail-closed），不回退到可预测默认密码/Token。
- 行情凭据（如 `LONGBRIDGE_APP_KEY/SECRET/ACCESS_TOKEN`）只授予行情权限；交易凭据独立、只授予必要交易权限。
- 模拟盘与实盘凭据不可复用。

## 5. 日志脱敏

- 日志、指标、错误 envelope、审计上下文不得输出连接串、认证头、签名、Provider 原始响应或 secret 值。
- Provider 响应/错误日志须过滤 token、签名、账户敏感字段。
- 指标标签受控，禁止把 UUID/symbol/错误全文作高基数标签。

## 6. 提交前与评审

```bash
make security-check     # 仓库策略扫描 + gitleaks（需安装 gitleaks）
git diff --cached       # 人工复查待提交内容
```

- PR 须说明新增网络访问、凭据类型、权限范围、日志脱敏、失败策略。
- 对 `.gitignore`、CI、部署、Adapter、认证、日志、网络监听的修改必须做安全评审。
- GitHub Actions `Security preflight` 是所有编译/测试 job 的前置依赖，失败则后续全部不启动。

## 7. 泄漏响应（摘要）

发现疑似泄漏立即停止发布：撤销/轮换凭据 → 隔离受影响环境并排查异常请求/资金操作 → 定位首次泄漏 commit 与传播范围 → 必要时重写 Git 历史并重新克隆（不能替代轮换）→ 记录事件与防复发门禁。任何人不得在 Issue/PR/聊天/日志粘贴疑似 secret 完整值，只留类型、来源和脱敏首尾字符。
