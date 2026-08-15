# 组合估值与行情集成实施计划

> 对应里程碑：M2（组合估值与行情集成）。
> 依赖：M1（BE-001、BE-002）、CONTRACT-005。

## 0. CONTRACT-005 行情服务批量精度查询 API 契约（已冻结，2026-08-05）

```http
POST /api/market-info/v1/instruments/precision:batch
```

请求：

```json
{
  "instrument_ids": [
    "0197c3ca-13e0-7b7d-8ff1-c6da977b31e2",
    "0197c3cc-4a4e-7c1c-8f13-b60fbdf6af98"
  ]
}
```

响应：

```json
{
  "items": [
    {
      "instrument_id": "0197c3ca-13e0-7b7d-8ff1-c6da977b31e2",
      "instrument_code": "instrument.bybit.spot.btc-usdt",
      "price_scale": 2,
      "quantity_scale": 6,
      "lot_size": "0.000001",
      "min_quantity": "0.0001",
      "as_of": "2026-08-05T00:00:00Z"
    }
  ],
  "missing_instrument_ids": []
}
```

设计要点：

- 用 `POST` 而非 `GET`：`instrument_ids` 列表规模不大（当前 3 个标的），但语义上是批量查询而非单资源获取，且避免受 URL 长度惯例限制；与现有 `GET /instruments`（单 `asset_code` 查询）风格并存，不取代它。
- `missing_instrument_ids` 显式列出请求了但目录里没有精度数据的 instrument，`backend` 据此直接触发 fail-closed，不需要自己从空结果推断。
- decimal 字段（`lot_size`/`min_quantity`）序列化为字符串，与仓库统一约定一致；`price_scale`/`quantity_scale` 是整数（小数位数），按数字传输。
- `as_of` 是该精度数据最近校验/采集的时间戳，供 `backend` 判断缓存新鲜度或排查数据陈旧问题，不强制要求逐次比较。
- 具体路由前缀/版本号需与 BE-004a 实现者最终对齐现有 `market-info-service` 路由注册方式（`07_api_and_admin_ui.md` §2.5 提到公共查询路由统一装配；实现记录见该文档 §2.4）。

## 1. BE-004a 行情服务补齐精度字段与批量查询（market-info-service 侧）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-004a | DONE（2026-08-05） | CONTRACT-005（已冻结） | 新增 `POST /instruments/precision:batch` 批量端点（未改动既有 `/instruments` 响应，见实现报告）；`FindInstrumentsByIDs` repository 方法；NVDA/QQQ/BTC-USDT 三个 Instrument 的种子数据 | API 契约测试覆盖批量查询与字段完整性 ✅；NVDA/QQQ 精度为长桥官方真实值 ✅；**BTC-USDT 精度四项仍是显式 `PLACEHOLDER`，未核实**（网络无法访问 Bybit API，见 `roadmap/01_decisions.md` DEC-003 未决附注），RM3/RM5 前必须核实 |

此任务修改 `market-info-service`（Go），与其余任务修改的 `backend`（Python）是不同目录，可独立并行、独立提交。

## 1.1 BE-003a 行情服务新增 CoinGecko FX Provider（market-info-service 侧，BE-003 前置）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-003a | DONE（2026-08-15） | DEC-006（已决策） | 新增 CoinGecko provider（历史/当前稳定币价格，公开接口、无需密钥，`internal/providers/coingecko/`）；`domain.AssetType`/`domain.InstrumentType` 枚举新增 `FX`，`core.assets`/`core.instruments` 的 `CHECK` 约束同步扩展（新 migration，见下）；新增 Asset+Instrument `asset.fx.usdt-usd` / `instrument.coingecko.fx.usdt-usd`（`asset_type=FX`，不进入任何组合的 `PortfolioMember`）；写入 `bars` 表时 `open=high=low=close=` 当日价格、`volume=0`，并在 `RawPayload`（落库后为 `market_bars.metadata.provider_payload`）显式标注 `ohlc_synthesized_from_single_price: true`；采集调度改为常驻：`internal/scheduler/incremental.go` 的 `schedulingMarket` 把 `FX+FX` 归类到既有 "continuous"（7x24 UTC）市场轴，新增 migration 直接种入一条已启用的 `collection_subscriptions` 行，不依赖 backend 的组合成员驱动同步 | CoinGecko 429/5xx 走 `ports.ProviderErrorRateLimited`/`ProviderErrorTemporaryUnavailable` 分类，由既有 `ingestion/retry.go` 退避重试，重试耗尽后落为可见的 `failed` 任务而非静默丢弃 ✅；`FX` 类型不会被 `ValidateAssetInstrument`/`schedulingMarket`/`providerScopeFor` 误判为未知组合（新增单测覆盖三处）✅；`internal/providers/coingecko` 单测覆盖 91.5%，全仓库覆盖率 88.3% ✅；`go test ./... -race` 全绿 ✅；针对真实 Postgres 跑通 `make test-integration`（含新 migration 与 core 种子脚本）✅ |
| **迁移策略提醒** | — | — | `Instrument.instrument_type` 枚举扩展需要一条新 migration（`CHECK` 约束或枚举类型），遵循 `project-conventions.md` §1"进入共享环境后只允许新增 migration，不得修改历史文件" | — |

## 2. BE-003 组合估值与快照模型

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-003 | DONE（2026-08-15） | BE-002、DEC-002、BE-003a | `repository/valuation.py`（`SqlPositionRepository`/`SqlCashBalanceRepository`/`SqlValuationSnapshotRepository`/`SqlPerformanceSnapshotRepository`，四个新定义在 `domain/valuation/models.py` 的 Protocol 的实现，幂等 upsert）；`adapters/market_data/asset_instrument_resolver.py`（`StaticAssetInstrumentResolver` + `resolve_fx_instrument`，落地 BE-004 遗留的 `AssetInstrumentResolver` 静态映射，同时解决 BE-003 的 FX 查询）；`application/valuation/service.py`（`SqlValuationService`，`ValuationService` 的具体实现，`/bars` 单次 `order=desc,limit=1` 查询实现 DEC-002 日切/结转，sync Protocol 内部通过 `asyncio.run` 桥接 async `MarketInfoClient`，误用时 fail-fast 抛 `AsyncBridgingError` 而非静默死锁）；`domain/valuation/performance.py`（`PerformanceSnapshot` 落地 + 最小化的 `compute_performance_snapshot`，仅计算 `total_return_pct`/`max_drawdown_pct`/`annualized_volatility`，`sharpe_ratio`/`sortino_ratio`/`benchmark_return_pct` 显式留空，完整报告生成留给 BT-006/BT-007） | 单测覆盖正常/边界/失败路径（含 `httpx` 无网络访问的 fake `MarketInfoClient`）✅；针对真实 Postgres 的集成测试覆盖四个新 repository 的幂等 upsert/round-trip ✅；汇率或价格完全缺失时 fail-closed（`MissingPriceDataError`/`UnsupportedFxPairError`/`UnresolvedInstrumentError`，不产出快照、不假设 1:1）✅；非交易日通过结转上一收盘价正确标记 `stale`，组合级取最保守值 ✅；`ruff format --check`/`ruff check`/`mypy backend`/`pytest -q` 全绿（337 passed 含集成测试，或无 DB 时 304 passed + 33 skipped）✅ |

## 3. BE-004 行情服务集成契约（backend 客户端）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-004 | DONE（2026-08-11） | BE-001、BE-004a | `adapters/market_data` 客户端（`httpx.AsyncClient`）：最新价/K 线/instrument/批量精度查询、`PrecisionCache`（TTL + fail-closed）、订阅同步（`compute_desired_instruments` + `sync_collection_subscriptions`，创建/重新启用/禁用，API 无 DELETE 故不删除行） | 契约测试（30 个，`httpx.MockTransport`）✅；精度缓存过期/不可用时 fail-closed（`PrecisionUnavailableError`，不返回旧值/默认值）✅；订阅同步正确 diff 三种状态 ✅ |
| **BE-004 遗留待办（不阻塞已完成范围，交给下一个消费者处理）** | 已由 BE-003 解决（2026-08-15） | — | `asset_id`（如 `crypto:bybit:BTC-USDT`）→ 行情服务 `instrument_code`/`provider` 的解析没有任何地方实现——`AssetInstrumentResolver` 故意设计成一个 `Protocol`，由调用方注入具体实现，而不是猜一个映射公式。BE-003 提供了 `adapters/market_data/asset_instrument_resolver.StaticAssetInstrumentResolver`：基于 DEC-001 冻结的固定标的清单的静态映射表（非动态发现），并在模块文档中明确标注「标的清单变为动态时需重新评估」 | — |

## 4. M2 退出门禁

- 组合当前配置、现金比例、净值可生成，且逐项可对账。
- 汇率或精度数据缺失时系统明确拒绝产出结果，不使用伪造默认值。
- `backend` 与 `market-info-service` 之间只通过 HTTP API 交互，不共享数据库表（`project-conventions.md` §2）。
- NVDA、QQQ、BTC-USDT 三个标的的精度规则可通过一次批量调用查询完整。
- `USDT→USD` 历史汇率可通过 `/bars` 查询到完整的每日序列，覆盖回测所需的时间区间。
