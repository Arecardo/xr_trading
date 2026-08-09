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

## 2. BE-003 组合估值与快照模型

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-003 | TODO | BE-002、DEC-002 | `Position`、`CashBalance`、`ValuationSnapshot`、`PerformanceSnapshot` 落地；支持原币值 + 汇率 + 基础货币折算；按 DEC-002 日切规则（UTC 自然日、非交易日 `price_status: stale`）生成净值快照 | 给定持仓/现金/汇率/价格，逐项对账净值；汇率缺失时不产出伪精确净值；非交易日快照正确标记 `stale` 且不误报缺失 |

## 3. BE-004 行情服务集成契约（backend 客户端）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-004 | TODO | BE-001、BE-004a | `adapters/market_data` 客户端（`httpx.AsyncClient`）调用行情服务的最新价/K 线/instrument/批量精度查询；采集资产范围由组合成员/持仓/基准驱动并同步给行情服务的订阅；批量精度查询结果做 TTL 本地缓存，过期或不可用时 fail-closed（拒绝下单，不用默认值兜底） | 契约测试（对 fake/录制响应）；采集范围随组合成员变化正确更新；缓存过期后强制刷新；行情服务不可达时下单请求被拒绝而非静默放行 |

## 4. M2 退出门禁

- 组合当前配置、现金比例、净值可生成，且逐项可对账。
- 汇率或精度数据缺失时系统明确拒绝产出结果，不使用伪造默认值。
- `backend` 与 `market-info-service` 之间只通过 HTTP API 交互，不共享数据库表（`project-conventions.md` §2）。
- NVDA、QQQ、BTC-USDT 三个标的的精度规则可通过一次批量调用查询完整。
