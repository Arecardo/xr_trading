# 仓库级项目规范

> 适用范围：整个 XR-Trading 仓库（`backend/`、`frontend/`、`market-info-service/`、`doc/`、`scripts/`）。
> 规范状态：第一版。
> 最近更新：2026-08-04

## 1. 单一真相来源

- **需求真相**：`doc/ai_quant_trading_system_requirements.md`。
- **设计真相**：`doc/technical/` 下对应模块文档。
- **数据库真相**：版本化 migration（Go 服务为 `market-info-service/internal/database/migrations/sql/`）。
- **规范真相**：本目录 + `doc/technical/09/12` 两份既有规范。

设计文档与实现不一致，视为缺陷；改代码同 PR 内同步改文档。

## 2. 两套代码库的边界纪律

项目当前存在成熟度差异较大的两套代码，必须避免「核心弱于外围」的漂移：

- `market-info-service/`（Go）**只负责行情**：采集、标准化、存储、查询。**不得**承载账户、订单、成交、资金、策略、风控、组合估值逻辑。
- `backend/`（Python）负责核心业务域：用户、组合、成员、账户绑定、持仓、估值、策略编排、风控、执行编排。
- 两者通过 **HTTP API 契约** 交互，不共享数据库表；跨服务读取优先走 API。行情服务的采集资产范围应由组合成员/持仓/基准驱动（见需求文档待办项）。
  - **协议升级触发条件**（RM0 DEC-003，2026-08-05）：默认使用 HTTP JSON，不预先引入 gRPC/protobuf。理由：两服务语言不同（Go/Python）无法直接共享代码库，当前调用场景（如精度字段查询）低频小payload，gRPC 收益不明显，且双协议会带来持续的契约同步维护成本。触发条件：当某个具体端点出现真实的高吞吐/低延迟需求（如 RM2 回测引擎批量搬运多年分钟线、或未来实时行情流式推送）时，**针对该端点**评估引入 gRPC/流式接口，不做全局协议切换。该端点两侧实现的取整/精度算法须保持逐位一致，建议用一组双语言共用的黄金测试用例（golden test vectors）驱动一致性验证，而非共享代码库。
- 即使同一供应商（如长桥）同时提供行情与交易，行情适配器与交易适配器**必须使用不同接口、权限范围和凭据**。

## 3. 目录组织原则

- 代码按**领域边界**组织，不按技术分层堆在单文件里。`backend/app.py` 的单体形态是历史原型，升级时须拆分为领域包（见 Python 后端规范）。
- 新增模块前，先确认它属于 `market-info-service` 还是 `backend`，避免职责错放。
- 配置结构进仓库，环境秘密不进仓库（见安全规范）。

## 4. 命名与标识约定

- 资产统一使用内部 `asset_id`（如 `equity:nasdaq:NVDA`、`crypto:bybit:BTC-USDT`、`cash:USD`），**禁止用 `symbol` 作为唯一键**。
- Go 服务实体用 UUIDv7 主键 + 唯一可读 `code`；数据库关系只用 UUID。
- 时间内部统一 UTC，对外 API 用 RFC 3339；金额/价格/成交量用定点十进制，JSON 序列化为字符串。
- 环境取值固定为 `research` / `backtest` / `paper` / `live`，默认不得为 `live`。

## 5. Git 与提交约定

- 分支：`master` 为主干；功能分支按 `feat/<模块>-<简述>`、`fix/<...>`、`refactor/<...>`、`docs/<...>`。
- 提交信息使用 **Conventional Commits**：`type(scope): subject`，type ∈ `feat|fix|refactor|docs|test|chore|security`，scope 用模块名（如 `market-info`、`backend`、`risk`）。
- 一个提交只做一件逻辑上内聚的事；正文说明动机、影响面、新增网络/凭据/权限。
- 提交前运行 `make security-check` 并检查 `git diff --cached`，禁止提交密钥、临时数据库、覆盖率产物、构建产物、`.DS_Store` 等编辑器/系统文件。
- API、migration、配置变化时，同 PR 更新对应设计文档与示例。

## 6. 环境隔离约定（全仓库强制）

| 环境 | 用途 | 允许真实下单 |
| --- | --- | --- |
| `research` | 数据探索、策略实验 | 否 |
| `backtest` | 历史回测 | 否 |
| `paper` | 模拟组合与模拟交易 | 仅模拟通道 |
| `live` | 小资金真实交易 | 是，须按组合授权 + 人工确认 |

- `live` 启用需同时满足：`trading.mode==live`、`allow_live_trading==true`、组合 `execution_mode==live`、独立实盘凭据、`require_manual_confirm==true`、用户显式确认记录。
- 模拟与实盘凭据不可复用；启动做防呆检查，缺失关键凭据时 fail-closed。

## 7. 测试与质量基线

- Go 服务：statement coverage >= 80%，并发/租约/调度改动跑 `go test -race`。
- Python 后端：核心领域（组合、风控、回测、执行编排、对账）必须有单元测试；不对一次性原型代码强加覆盖率门槛，但升级为正式模块后须补齐（见 Python 规范）。
- 每个 PR 至少包含一个**验证步骤**（单测、集成测试、对账校验或回测可复现性校验）。

## 8. 文档写作约定

- 每篇设计文档头部标注：来源、状态、最近更新日期。
- 用表格/JSON 示例固化数据结构与状态机；示例只用明显无效的占位值。
- 新增文档挂到对应 `README.md` 索引，保持可导航。
