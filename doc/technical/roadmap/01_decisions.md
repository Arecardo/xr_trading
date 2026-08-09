# P0 关键决策解锁（RM0）

> 目的：关掉阻塞下游的待定项。成本低、解锁面大，先于任何编码完成。
> 来源：需求文档 §14、行情服务总览 §15 的待确认/待讨论项。
> 状态：已完成（DEC-001/002/003/005 全部 DONE；DEC-004 降级为 P1 后置，标记 READY）。最近更新：2026-08-05。

这些任务产出是**书面结论**（写回对应设计文档或本目录），不是代码。除 DEC-004 外均为 RM1/RM2 的前置。

## 任务清单

### DEC-001 冻结首批标的清单 — P0 — **DONE（2026-08-05）**
- 依赖：无
- 输出：首批标的锁定为 `equity:nasdaq:NVDA`（held）、`equity:nasdaq:QQQ`（held，兼作宽基基准）、`crypto:bybit:BTC-USDT`（held，现货，非合约）。全部 `asset_id`/venue/quote_currency/provider_symbols 定义见需求文档 §4.4 与 `doc/technical/03_universe_data.md` §2、§3。
- 关键澄清：
  - 加密标的为 **现货** BTCUSDT（计价 USDT），不做永续合约/杠杆，与 §2.2、§4.1 非目标一致；USDT→USD 折算走 §4.2 通用汇率记录规则，不做 1:1 硬编码假设。
  - 候选标的管理复用现有 `Portfolio`/`PortfolioMember`（支持按条件筛选 + 手工添加），不复活已废弃的 `stock_pools`。
  - QQQ 实际挂牌 NASDAQ（非 NYSE Arca），venue 字段已按真实挂牌地填写。
- 测试：`asset_id`、venue、quote_currency、provider_symbols 映射对长桥（`NVDA.US`/`QQQ.US`）/Bybit（`BTCUSDT`）均可解析。
- 完成条件：清单足以支撑一次端到端纵切；候选与持有状态区分清楚。✅ 已满足。

### DEC-002 定组合估值日切时间与统一时区 — P0 — **DONE（2026-08-05）**
- 依赖：无
- 输出：`valuation_timezone: UTC`，`valuation_cutoff: 00:00`。理由：晚于美股夏令时/冬令时收盘（20:00/21:00 UTC）3–4 小时缓冲；与 Bybit 原生 UTC 日K边界对齐；固定 UTC 时刻不受美股 DST 切换影响。详见需求文档 §5.5.1、`02_config_environment.md`。
- 关键澄清：按 **UTC 自然日**（365 天/年）生成 `ValuationSnapshot`，不因美股非交易日跳过；美股非交易日沿用上一交易日收盘价并标记 `price_status: stale`。此规则同时约束 RM2 回测引擎必须按自然日而非交易日推进，以捕捉加密资产周末波动对组合风险的影响。
- 展示时区 `environment.timezone` 由占位符 `Asia/Kuala_Lumpur` 改为 `Asia/Shanghai`（仅影响报告展示转换，不影响估值计算基准）。
- 测试：跨美股交易日历与加密 24/7 时间轴，同一日切能生成一致净值快照；非交易日快照正确标记 `stale`。
- 完成条件：回测与 paper 估值使用同一日切定义，无歧义。✅ 已满足。

### DEC-003 定碎股与加密精度规则 — P0 — **DONE（2026-08-05）**
- 依赖：DEC-001
- 输出：
  1. **碎股（2026-08-05 修订）**：`research`/`backtest` 内部按 `Decimal` 任意精度记录数量，不整股取整（首批组合仅约 2,500 美元，整股取整会让目标权重严重失真）。`paper`/`live` 必须遵守 `Instrument` 目录里的真实 `lot_size`——BE-004a 采集到长桥官方规则「暂不支持碎股交易」后确认 `paper` 走长桥模拟盘 API 真实下单、与 `live` 同一套接口，不能自行放宽。原表述「`research`/`backtest`/`paper` 均不整股取整」已修正，详见需求文档 §5.1.1 修订说明；验证计划是等 BT-003/BT-006 落地后跑两轮回测（受限 vs 不受限）对比跟踪误差再决定后续应对。
  2. **精度规则不硬编码**：唯一来源是 `market-info-service` 的 `Instrument` 目录（`price_scale`/`quantity_scale`/`lot_size`/`min_quantity`），`backend` 通过 API 读取；数据缺失/过期时下单前 fail-closed，不用默认值兜底。
  3. **跨服务交互方式**：维持 HTTP JSON + 批量端点 + TTL 缓存，不引入 gRPC/protobuf；协议升级触发条件写入 `.claude/standards/project-conventions.md` §2（按具体高吞吐端点评估，不做全局切换）。
  - 详见需求文档 §5.1.1、`doc/technical/03_universe_data.md` §8。
- **未决附注（2026-08-05）：BTC-USDT 精度数据待核实**。BE-004a 完成后 NVDA/QQQ 精度已是真实值，BTC-USDT 的 `price_scale`/`quantity_scale`/`lot_size`/`min_quantity` 四项仍是占位值——本地网络与本仓库沙箱环境均连不上 Bybit API，判断为环境性/临时限制（非 Bybit 对该地区的结构性屏蔽，已与用户确认），暂不改变加密货币支持范围。**不阻塞 RM2**（`research`/`backtest` 用 `unrestricted` 精度模式，不读取这四个字段）；**RM3/RM5 前必须核实**（`paper`/`live` 用 `restricted` 模式，需要真实值），届时若仍连不上需重新评估是否为结构性限制。
- 测试：下单数量/价格按规则取整后仍满足最小交易量与精度约束；目录数据缺失时下单请求应被拒绝而非静默使用默认值。
- 完成条件：撮合与下单模块有明确的数量/精度规则可依。✅ 已满足（数值本身由 RM1 前置任务采集写入，见需求文档 §5.1.1 待办项）。

### DEC-004 定最新行情采集方式 — P1（非纵切阻塞，可后置）
- 依赖：无
- 输出：最新价用定时轮询还是 WebSocket，及其与 K 线采集的关系；美股盘前盘后是否纳入小时线。
- 测试：不适用（决策）。
- 完成条件：结论写入行情服务总览 §15，标注对采集调度的影响。

### DEC-005 定核心后端架构方向 — P0（关键）— **DONE（2026-08-05）**
- 依赖：无
- 输出：**RM1 起点即升级到 FastAPI + PostgreSQL + Alembic**，不再继续在 stdlib `http.server` + SQLite 原型上累加代码。
- 理由：
  1. `python-backend-standards.md` §3 已有的升级触发条件（"需要引入…正式模块"）已被 RM1 自身退出条件命中（组合估值/持仓/现金/汇率快照骨架 + 行情服务集成属于正式领域模块，非一次性研究脚本），不是预判未来的假设性需求。
  2. 现在切换成本最低：`backend/app.py`（1023 行）尚无 `requirements.txt`/`pyproject.toml`，没有值得保留的正式领域代码，晚切换等于同一批业务逻辑写两遍、migration 重新设计一次。
  3. DEC-003 要求的批量精度查询 + TTL 缓存客户端，用 `httpx.AsyncClient` + FastAPI 的异步模型比 stdlib 同步阻塞模型更顺手。
  4. 运维基建可直接复用 `market-info-service/compose.yaml` 已验证的 Postgres 服务模式：backend 用独立数据库实例/角色，不与行情服务共享表，符合 `project-conventions.md` §2 既有边界。
- 落地方式：`backend` 新增独立 Postgres 服务定义（复用现有 compose 模式，独立 `POSTGRES_DB`/角色）；持久化迁移采用 Alembic 版本化 migration，呼应仓库"数据库真相是版本化 migration"的约定（`project-conventions.md` §1）。范围仍收紧在 RM1 退出条件内（组合估值/持仓/现金/汇率快照骨架 + 行情服务集成），不因换框架顺带扩大到策略/风控/执行模块（那是 RM2/RM3 的范围）。
- 测试：不适用（决策）。
- 完成条件：写入本目录与 python 后端规范；RM1 据此展开，避免核心弱于外围的漂移。✅ 已满足，详见 `.claude/standards/python-backend-standards.md` §3。

## 退出条件（RM0）

DEC-001、002、003、005 全部产出书面结论并写回对应文档；DEC-004 可标记 `READY` 后延。**RM0 退出条件已满足（2026-08-05）**，可进入 RM1（后端地基）与 RM2（回测引擎）并行阶段。
