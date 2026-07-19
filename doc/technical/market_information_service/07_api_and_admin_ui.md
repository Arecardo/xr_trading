# 市场资讯服务 API 与前端管理页面设计

> 来源：拆分自 `../11_market_information_database.md` 与 `../10_market_information_service.md`。

## 1. API 通用约束

- 行情查询只读取已落库数据，不因一次查询隐式触发采集任务。
- 写接口只管理配置和采集任务，不允许直接写入、修改或删除行情记录。
- 每条行情都保留 `instrument_id`、`provider_instrument_id` 和 `source`，不同来源的数据不隐式合并或互相覆盖。
- 服务内部和数据库关系使用 UUID；对外查询同时支持稳定、可读的业务编码。
- 时间统一使用 UTC ISO 8601 格式；价格、成交量等 decimal 字段使用规范化字符串序列化，避免浮点精度损失。规范化结果不保证保留无意义的尾随零，例如 `"62349.80"` 会返回 `"62349.8"`；展示精度由前端依据 Instrument 精度格式化。
- 列表接口原则上采用游标分页；最新行情接口返回按 Asset/Instrument 映射自然有界的多来源快照集合，首期不分页。所有写操作均需鉴权并记录操作人和审计上下文。
- API 版本首期统一使用 `/api/market-info/v1` 前缀。
- JSON 请求体只允许一个 JSON 值、拒绝未知字段，默认大小上限为 1 MiB；超过端点定义的上限返回 `400 INVALID_ARGUMENT`。
- Request ID 使用 `req_<UUIDv7>`。调用方提供合法值时沿用，否则服务重新生成；所有响应均通过 `X-Request-ID` 返回。
- 游标是带版本和查询 scope 的 URL-safe 不透明值，只能传回生成它的同一类查询，调用方不得解析、修改或跨接口复用。

## 2. 公共行情查询 API

### 2.1 最新行情

```http
GET /api/market-info/v1/quotes/latest
```

支持以下查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `asset_code` | 条件必填 | 资产稳定可读编码，例如 `asset.crypto.btc`、`asset.equity.us.aapl` |
| `instrument_code` | 条件必填 | 交易标的稳定可读编码，例如 `instrument.bybit.spot.btc-usdt` |
| `provider` | 否 | Provider 编码，例如 `bybit` |

`asset_code` 与 `instrument_code` 至少提供一个：

- 只传 `asset_code` 时，返回该资产下所有可用 Instrument 和 Provider 的最新行情列表。
- 传 `instrument_code` 时，返回该 Instrument 的全部可用来源。
- 同时传 `asset_code` 与 `instrument_code` 时，Instrument 必须属于该 Asset，否则返回 `400 INVALID_ARGUMENT`。
- 同时传 `provider` 时，进一步限定到指定来源。
- 多来源结果是相互独立的行情，不代表计价币、市场口径和用途相同。
- ProviderInstrument 必须启用、具备 quote capability 且位于 `[valid_from, valid_to)` 有效期内；Provider 为 `active` 或 `degraded` 时可查询，`disabled` 时返回空结果。
- 已知 Asset/Instrument 但没有匹配行情时返回 `200` 和空 `quotes`；未知 Asset/Instrument 分别返回 `404 ASSET_NOT_FOUND` / `404 INSTRUMENT_NOT_FOUND`。
- 参数格式、编码组合、未知 Provider 或只传 Provider 时返回 `400 INVALID_ARGUMENT`。
- 每条 ProviderInstrument 快照独立返回并按 Instrument code、Provider code、ProviderInstrument code 稳定排序；即使属于同一 Provider 也不合并。
- `quality_status` 原样反映数据库中最新快照的质量状态，不静默回退到更旧的 `valid` 行情。调用方可结合该字段和 `market_time` 判断是否采用。
- 该集合按 Asset/Instrument 当前有效映射自然有界，首期一次返回且不提供 `limit` / `cursor`；查询过程不会触发实时 Provider 请求或采集任务。

示例：

```http
GET /api/market-info/v1/quotes/latest?asset_code=asset.crypto.btc
```

```json
{
  "asset": {
    "asset_id": "019...",
    "asset_code": "asset.crypto.btc",
    "asset_type": "crypto"
  },
  "quotes": [
    {
      "instrument_id": "019...",
      "instrument_code": "instrument.bybit.spot.btc-usdt",
      "provider": "bybit",
      "provider_instrument_id": "019...",
      "provider_instrument_code": "provider.bybit.spot.btcusdt",
      "provider_symbol": "BTCUSDT",
      "price": "62350.12",
      "bid_price": "62349.8",
      "bid_size": "0.42",
      "ask_price": "62350.2",
      "ask_size": "0.35",
      "open_24h": "61000",
      "high_24h": "63000",
      "low_24h": "60500",
      "base_volume_24h": "15234.8",
      "quote_volume_24h": "941234567.8",
      "quote_currency": "USDT",
      "market_time": "2026-07-02T08:00:00Z",
      "received_at": "2026-07-02T08:00:01Z",
      "quality_status": "valid"
    }
  ]
}
```

### 2.2 K 线查询

```http
GET /api/market-info/v1/bars
```

必填参数：

- `instrument_code`
- `provider`
- `interval`

可选参数包括 `start_time`、`end_time`、`limit`、`order` 和分页游标。时间范围采用 `[start_time,end_time)`：起点包含、终点排除，也允许只提供单侧边界。K 线接口不接受仅以 `asset_code` 定位数据，因为资产本身不能确定交易市场、交易对、计价币和来源口径。

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `instrument_code` | 是 | Instrument 稳定可读编码 |
| `provider` | 是 | Provider 稳定编码；后端解析为唯一选中的 ProviderInstrument |
| `interval` | 是 | 首期为 `1h` 或 `1d`，并且必须存在于选中映射的 capabilities 中 |
| `start_time` | 否 | RFC3339 时间，包含边界 |
| `end_time` | 否 | RFC3339 时间，排除边界；必须晚于 start_time |
| `order` | 否 | `asc` 或 `desc`，默认 `desc` |
| `limit` | 否 | 默认 200，最大 1000 |
| `cursor` | 否 | 绑定 Instrument、Provider、interval、order 和时间范围的不透明游标 |

来源与分页规则：

- 同一 Instrument/Provider 有多条当前有效映射时，按 `is_default DESC, priority ASC, provider_instrument_code ASC` 选中一条，与 `/instruments` 下拉选项保持一致。
- 选中的映射必须启用、当前有效且 `historical = true`；请求 interval 不在该映射 capabilities 中时返回 `400 UNSUPPORTED_INTERVAL`，不自动切换其他映射。
- 未知 Instrument 返回 `404 INSTRUMENT_NOT_FOUND`；未知 Provider、不可用来源组合或缺少必填字段返回 `400 INVALID_ARGUMENT`；非法时间范围返回 `400 INVALID_TIME_RANGE`。
- 只读取 `is_current = true` 的 K 线 revision。相同开盘时间不会返回旧 revision，也不会跨 ProviderInstrument 合并。
- 游标位置为上一页最后一根 K 线的 `open_time`。升序使用严格 `open_time > cursor`，降序使用严格 `open_time < cursor`，不会重复页边界。
- 后续页必须重复发送与第一页完全相同的查询范围和排序参数；游标跨查询复用或篡改返回 `400`。
- `quality_status` 原样返回，不静默替换或丢弃当前 revision。

示例：

```http
GET /api/market-info/v1/bars?instrument_code=instrument.bybit.spot.btc-usdt&provider=bybit&interval=1h&start_time=2026-07-01T00:00:00Z&end_time=2026-07-02T00:00:00Z
```

```json
{
  "instrument": {
    "instrument_id": "019...",
    "instrument_code": "instrument.bybit.spot.btc-usdt",
    "base_asset_code": "asset.crypto.btc",
    "quote_asset_code": "asset.cash.usdt",
    "quote_currency": "USDT"
  },
  "provider": {
    "provider_code": "bybit",
    "provider_instrument_id": "019...",
    "provider_instrument_code": "provider.bybit.spot.btcusdt",
    "provider_symbol": "BTCUSDT"
  },
  "interval": "1h",
  "order": "desc",
  "bars": [
    {
      "open_time": "2026-07-02T07:00:00Z",
      "close_time": "2026-07-02T08:00:00Z",
      "open": "62180.1",
      "high": "62420",
      "low": "62120.5",
      "close": "62350.12",
      "volume": "152.834",
      "quote_volume": "9512345.67",
      "trade_count": 12450,
      "revision": 1,
      "is_closed": true,
      "quality_status": "valid",
      "provider_updated_at": "2026-07-02T08:00:01Z",
      "collected_at": "2026-07-02T08:00:02Z"
    }
  ],
  "next_cursor": null
}
```

### 2.3 Instrument 与 Provider 可选项

```http
GET /api/market-info/v1/instruments?asset_code=asset.crypto.btc&enabled=true
```

该接口为查询页面提供联动选项，响应中包含 Instrument、可用 Provider、是否为默认来源以及支持的周期。前端选择顺序为：

```text
Asset -> Instrument -> Provider -> Interval
```

查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `asset_code` | 是 | Asset 稳定可读编码 |
| `enabled` | 否 | 省略时默认为 `true`；首期只接受 `true`，不通过公共联动接口暴露禁用配置 |
| `limit` | 否 | 默认 50，最大 100；分页单位为 Instrument |
| `cursor` | 否 | 绑定当前 `asset_code` 的不透明游标，不得跨资产复用 |

默认值规则：

- Instrument 默认选择当前资产下第一个启用且有可用行情来源的 Instrument。
- Provider 优先选择该 Instrument 的 `is_default = true` 来源；未设置时按 `priority` 从小到大选择。
- Interval 首期默认选择 `1h`。
- 前端必须把选中的默认值作为明确参数发送给后端；`/bars` 不设置隐式 Provider 默认值。
- Provider 下拉框只展示当前 Instrument 已启用且有效的 Provider，Interval 下拉框由该 ProviderInstrument 的 `capabilities.intervals` 生成。
- Instrument、ProviderInstrument 的有效期按请求时刻使用 `[valid_from, valid_to)` 判断；Provider 为 `active` 或 `degraded` 时可选，`disabled` 时排除。
- Instrument 展示名优先读取 `metadata.display_name`，缺失时使用 Instrument 的 `symbol`。
- 若同一 Instrument 下同一 Provider 暂时存在多条有效映射，按 `is_default`、`priority`、ProviderInstrument code 选择最优一条，Provider 下拉框不重复展示。
- Instrument 按 `instrument_code` 稳定升序分页；Provider 按默认来源优先、`priority` 升序、Provider code 升序返回。

```json
{
  "items": [
    {
      "instrument_id": "019...",
      "instrument_code": "instrument.bybit.spot.btc-usdt",
      "display_name": "BTC/USDT",
      "providers": [
        {
          "provider_code": "bybit",
          "display_name": "Bybit",
          "is_default": true,
          "priority": 10,
          "supported_intervals": ["1h", "1d"]
        }
      ]
    }
  ],
  "next_cursor": null
}
```

### 2.4 公共查询路由与只读边界

- `/instruments`、`/quotes/latest`、`/bars` 作为一组公共查询路由统一装配，生产入口与数据库集成测试使用同一注册函数，避免测试路由和真实服务漂移。
- 三个端点首期只接受 `GET`，不经过管理权限中间件；其他方法由路由层返回 `405 Method Not Allowed`。
- 公共查询统一经过 Request ID 中间件，并使用相同的 JSON、错误 envelope、UTC、decimal 和游标约束。
- 查询链路只允许调用 query service 和只读 repository，不调用 Adapter、Scheduler、Worker 或任务写 repository。
- PostgreSQL 集成验收以同一批 Asset、Instrument、两个 Provider、两个 ProviderInstrument、最新行情和 K 线数据贯穿三个 HTTP 端点；查询前后核对订阅、Run、Task、checkpoint、最新行情、K 线和质量问题表，必须全部无变化。
- API 样例是字段和数据类型契约；UUID 与时间值会随数据变化，decimal 按规范化字符串返回，前端不得依赖示例中的固定字符串长度或小数位数。

## 3. Provider 状态 API

```http
GET /api/market-info/v1/providers/status
```

该接口供管理页面观察数据源运行状态，只汇总数据库中已有的任务和行情统计。请求期间不得实时调用外部 Provider，以免页面访问消耗供应商限流额度或受外部网络阻塞。

Provider 状态区分：

- `configured_status`：人为配置状态，包括 `active`、`disabled` 和 `degraded`。
- `health_status`：根据近期采集任务和行情新鲜度计算，包括 `healthy`、`degraded`、`unhealthy` 和 `unknown`。

```json
{
  "items": [
    {
      "provider_code": "bybit",
      "display_name": "Bybit",
      "provider_type": "exchange",
      "configured_status": "active",
      "health_status": "healthy",
      "last_success_at": "2026-07-02T08:00:03Z",
      "last_failure_at": null,
      "consecutive_failures": 0,
      "checked_at": "2026-07-02T08:00:10Z",
      "scopes": [
        {
          "market": "crypto_spot",
          "session_type": "continuous",
          "interval": "1h",
          "market_state": "open",
          "health_status": "healthy",
          "freshness_status": "fresh",
          "data_delay_seconds": 3,
          "active_subscriptions": 20,
          "delayed_subscriptions": 0,
          "next_market_open_at": null
        }
      ]
    }
  ]
}
```

首期状态规则：

- `disabled` 的 Provider 不参与健康判断。
- 启用后尚无足够采集记录时为 `unknown`。
- 近期任务成功且关键订阅均在允许延迟内时为 `healthy`。
- 部分任务失败或部分订阅延迟，但仍可持续取得数据时为 `degraded`。
- 连续失败达到阈值或所有关键订阅均严重延迟时为 `unhealthy`。
- Provider 总体状态由各 scope 汇总产生，最终健康状态在 Service 层动态计算，不作为独立事实落库。

行情新鲜度首期采用简单规则：

- 美股只监控常规交易时段；开市期间根据最近一根应当完成的 K 线计算延迟。
- 美股每日休市、周末和交易所节假日期间停止计算延迟，返回 `market_state = closed`、`freshness_status = not_applicable` 和 `data_delay_seconds = null`，该 scope 不因休市被降级。
- 美股休市前已经缺失的数据进入数据质量与缺口修复流程，休市期间不持续累加实时延迟。
- 美股交易时间使用支持夏令时、节假日和提前休市的交易日历判断，不使用固定 UTC 时间。
- 加密货币现货按 7×24 小时连续计算，`session_type = continuous`。

SCH-002 已冻结美股 scope 的底层状态投影：核心时段为 `[open,close)`；休市时强制 `not_applicable` 且 delay 为 `null`；开市时使用最近一根达到 close delay 的期望 K 线，并以核心交易时长而非墙上时钟计算落后秒数。尚无成功数据时返回 `unknown + null`。首期日历仅支持经 NYSE 官方核验的 2026～2028，越界错误不得映射成 `closed/not_applicable`，应作为日历配置故障进入健康状态和告警。ADM-005 负责把该纯计算接到数据库统计和本接口。

首期不提供“立即探测 Provider”接口；实际采集任务作为 Provider 的健康观测来源。

ADM-005 首期冻结以下精确契约：

- 端点要求 `operations.read`，不接受查询参数，不分页；返回全部 Provider 并按 `provider_code` 排序，因此 `disabled` 或没有有效订阅的 Provider 也不会从管理页面消失。
- Repository 使用单次只读聚合查询读取 Provider、启用且当前有效并具备 historical/interval capability 的 Subscription、checkpoint，以及按 Subscription 聚合的 Task 最近成功/失败时间。查询不调用 Adapter、Scheduler 执行线或任何写 Repository，也不新增 migration。
- scope 固定按 `provider + market + interval` 分组；Bybit spot 映射为 `crypto_spot + continuous`，Longbridge US 股票/ETF 映射为 `us_equity + regular`。scope 按 market、session type、interval 稳定排序。
- 连续市场使用 SCH-001 的 UTC 最新闭合窗口；美股使用 SCH-002 的 NYSE 日历与核心交易时长。`data_delay_seconds` 是最新成功闭合 open time 落后于当前应有 open time 的市场时间秒数，多个订阅取最大值；`delayed_subscriptions` 是该 scope 的延迟订阅数。
- 单个订阅连续失败达到 3 次时 scope 为 `unhealthy`；所有订阅均严重延迟时也为 `unhealthy`，首期“严重延迟”定义为至少 3 个 interval。部分失败、部分延迟、未达到严重阈值的延迟，或已知/未知混合时为 `degraded`；全部无成功事实时为 `unknown`；其余为 `healthy`。
- 美股休市时 `market_state=closed`、`freshness_status=not_applicable`、`data_delay_seconds=null`，休市本身不降低健康状态；仍可由已持久化的连续失败事实降级。日历超出已核验年份不是正常休市，返回 `market_state=unknown`、`freshness_status=unknown`、scope/provider `unhealthy`。
- Provider 总体状态由 scope 动态汇总，优先级为 `unhealthy > degraded > healthy/unknown 混合 > healthy > unknown`。人为 `configured_status=degraded` 会把计算出的 healthy 限制为 degraded；`configured_status=disabled` 的 Provider 和 scope `health_status` 固定为 unknown，不参与健康判断。
- `last_success_at`、`last_failure_at` 和 `consecutive_failures` 分别取有效订阅中的最新成功、最新失败和最大连续失败次数；`checked_at` 是本次 Service 计算使用的同一个 UTC 固定时刻。

## 4. 采集订阅 API

采集订阅描述系统需要持续采集的 ProviderInstrument 和周期，不负责隐式创建历史回填任务。

### 4.1 查询订阅

```http
GET /api/market-info/v1/collection-subscriptions
```

支持按 `provider`、`instrument_code`、`interval` 和 `enabled` 筛选，并使用游标分页。响应同时返回 UUID、可读编码和 Provider 外部代码，避免前端额外拼接查询。

- 默认每页 50 条、最大 100 条，按 Subscription UUIDv7 升序；游标绑定全部筛选条件，跨条件复用返回 `400 INVALID_ARGUMENT`。
- 每项返回 `subscription_id`、Provider/Instrument/ProviderInstrument 编码、`provider_symbol`、interval、四个可变设置以及创建/更新时间；不返回内部 `metadata.audit_log`。
- 读取需要 `operations.read`。

### 4.2 创建订阅

```http
POST /api/market-info/v1/collection-subscriptions
```

```json
{
  "provider": "bybit",
  "instrument_code": "instrument.bybit.spot.btc-usdt",
  "interval": "1h",
  "enabled": true,
  "priority": 100,
  "close_delay_seconds": 120,
  "revision_delay_seconds": null,
  "reason": "enable hourly collection"
}
```

Service 层根据 `provider + instrument_code` 解析唯一的 `provider_instrument_id`。创建时必须满足：

- ProviderInstrument 存在、启用且仍在有效期内。
- `interval` 在 ProviderInstrument 的 `capabilities.intervals` 中。
- 同一 ProviderInstrument 和 interval 不重复；重复返回 `409 conflict`。
- `priority`、`close_delay_seconds` 和非空的 `revision_delay_seconds` 不得为负数。
- `enabled`、`priority` 和 `close_delay_seconds` 必须显式提供，避免字段遗漏被解释为禁用或零延迟；整数必须位于 PostgreSQL 对应类型范围内。
- `reason` 必填、去除首尾空白后不能为空，最长 512 个字符；创建需要 `subscriptions.manage`。

首期订阅不支持 `backfill_from`。创建或启用订阅后，从下一个 Scheduler 周期开始采集当前及之后的数据，不自动创建历史回填任务。

### 4.3 修改订阅

```http
PATCH /api/market-info/v1/collection-subscriptions/{subscription_id}
```

首期只允许修改 `enabled`、`priority`、`close_delay_seconds` 和 `revision_delay_seconds`。`provider`、`instrument_code`、`provider_instrument_id` 和 `interval` 构成订阅身份，不允许修改；如需更换，应禁用旧订阅并创建新订阅。

```json
{
  "enabled": false,
  "revision_delay_seconds": null,
  "reason": "pause collection during provider maintenance"
}
```

PATCH 至少提供一个可变设置，并且必须显式提供 `reason`。`revision_delay_seconds` 未提供表示保持不变，显式 `null` 表示关闭 revision pass，整数表示设置新延迟。未知字段（包括任何身份字段）统一拒绝。修改需要 `subscriptions.manage`。

- 禁用后 Scheduler 不再创建新任务。
- 禁用不会自动取消已经处于 `running` 的任务。
- 修改优先级和延迟配置只影响之后创建的任务。
- 首期不提供 DELETE，保留订阅配置和关联任务历史。
- 创建与每次修改都把 Principal、actor type、Request ID、reason 和 UTC 时间原子追加到该订阅的内部 `metadata.audit_log`；失败请求不写审计记录。

### 4.4 显式历史回填

```http
POST /api/market-info/v1/ingestion-runs/backfill
```

```json
{
  "provider": "bybit",
  "instrument_code": "instrument.bybit.spot.btc-usdt",
  "interval": "1h",
  "start_time": "2026-06-01T00:00:00Z",
  "end_time": "2026-07-01T00:00:00Z",
  "reason": "initialize historical data"
}
```

首期保持单一语义：

- 一次请求只接受一个 Instrument、一个 Provider、一个 interval 和一个连续时间范围。
- ProviderInstrument 对应的采集订阅必须已存在；是否要求订阅处于启用状态由 Service 层校验，首期建议必须启用。
- 一次请求只创建一个 `backfill` Run 和一个 Task，不支持传入 Instrument、Provider、interval 或时间范围数组。
- Worker 在同一个 Task 内完成 Provider API 分页和限流处理，不为每页批量创建 Task。
- `start_time < end_time`，二者均使用 UTC；不允许回填未来区间。
- 接口成功创建任务后返回 `202 Accepted`，不等待采集完成。

```json
{
  "run_id": "019...",
  "task_id": "019...",
  "status": "pending",
  "created_at": "2026-07-02T08:00:00Z"
}
```

并发提交完全相同且仍处于 `pending`、`running` 或 `retry_wait` 的回填范围时返回 `409 conflict`，防止页面重复点击创建等价任务。任务完成后允许再次对同一范围发起回填，用于数据修订。

ADM-002 已接入该接口：严格 JSON 解码拒绝未知字段及数组请求，路由要求 `ingestion.manage`，`requested_by`、actor type 和 Request ID 只能取自认证上下文，客户端仅显式提供 reason。输入中的 RFC3339 时间在进入 Service 前规范化为 UTC；成功响应直接返回唯一 Run/Task 身份并使用 `202 Accepted`。`ErrBackfillAlreadyRunning` 映射为 `409 BACKFILL_ALREADY_RUNNING`；找不到匹配的启用订阅映射为 `404 SUBSCRIPTION_NOT_FOUND`。接口不调用 Provider、不等待 Worker，也不提供批量或 `backfill_from` 语义。

## 5. 采集 Run 与 Task 管理 API

### 5.1 Run 列表与详情

```http
GET /api/market-info/v1/ingestion-runs
GET /api/market-info/v1/ingestion-runs/{run_id}
```

列表支持按 `run_type`、`trigger_type`、`status`、`requested_by`、`created_from` 和 `created_to` 筛选，并使用游标分页。列表返回 Run 汇总计数；详情返回 Run 信息和任务摘要，任务数量较多时使用独立 Task 列表分页，不在 Run 详情中无限展开。

Run 状态及 `task_count`、`success_count`、`failed_count` 由 Service 层根据 Task 汇总校正，数据库中的 Task 状态仍是最终事实来源。

ADM-003 冻结以下查询语义：

- 默认每页 50、最大 100，按 Run UUIDv7 降序返回最新记录；游标绑定全部筛选条件。`created_from` 包含、`created_to` 排除，时间在进入 Application 前规范化为 UTC。
- `status` 筛选也使用 Task 状态聚合产生的当前 Run 状态，不使用可能滞后的 Run 缓存状态。因此 Worker 刚修改 Task、Run 汇总写尚未完成时，列表筛选和响应仍保持一致。
- Run 列表和详情返回 `pending/running/retry_wait/success/failed/canceled` 全部 Task 计数；`started_at/finished_at` 取 Task 聚合结果。详情不内嵌无限 Task 数组，前端使用 Task 列表的 `run_id` 筛选加载。
- Run `context` 只公开 `trigger`、reason、actor type、Request ID、Provider/Instrument/interval 和范围等白名单字符串；不返回原始 `error_summary` 或未知 context 字段。
- 列表与详情均需要 `operations.read`；未知 Run 返回 `404 NOT_FOUND`。

### 5.2 Task 列表与详情

```http
GET /api/market-info/v1/ingestion-tasks
GET /api/market-info/v1/ingestion-tasks/{task_id}
```

列表支持按 `run_id`、`status`、`provider`、`instrument_code`、`interval`、`created_from` 和 `created_to` 筛选。详情返回：

- Run、订阅、Instrument 和 Provider 的 UUID 与可读编码。
- 采集时间范围、当前状态、`attempt_count` 和 `max_attempts`。
- `next_attempt_at`、租约持有者和租约到期时间。
- 开始与结束时间、Provider request ID。
- 标准化错误码、错误摘要和可展示的错误详情。
- `retry_of_task_id`；如该任务被管理员重新执行，也可返回其后续手动重试任务链接。

API 不得返回 Provider token、secret、签名内容、数据库连接信息或完整堆栈。面向前端的 `error_details` 必须经过脱敏。

ADM-003 的 Task 查询约束如下：

- 默认每页 50、最大 100，按 Task UUIDv7 降序；游标绑定 `run_id`、status、Provider、Instrument、interval 和创建时间范围。`created_from` 包含、`created_to` 排除。
- 列表与详情使用同一联表读模型，返回 Run、Subscription、Provider、Instrument、ProviderInstrument 的 UUID 和可读编码，以及 Provider 外部 symbol；不会为每条 Task 再发起目录查询。
- 响应包含范围、attempt、retry/lease、执行时间、Provider request ID、取消字段和 `retry_of_task_id`。ADM-004 创建新 Task 后，新 Task 可通过该字段定位原任务；ADM-003 首期不在原任务详情中额外返回无界的后继数组。
- 原始 `error_message` 不对外返回。Service 根据标准化 `error_code` 生成固定可展示的 `error_summary`，未知 code 统一降级为 `internal_error`；`error_details` 首期只允许通过合法的 `provider_code`，未知字段、非法 JSON、控制字符和疑似凭据全部丢弃。
- 列表与详情需要 `operations.read`；未知 Task 返回 `404 TASK_NOT_FOUND`。查询只读 PostgreSQL，不调用 Provider，也不修改行情、Task 或 Run 状态。

### 5.3 手动重试失败任务

```http
POST /api/market-info/v1/ingestion-tasks/{task_id}/retry
```

```json
{
  "reason": "credentials renewed"
}
```

- 只允许重试 `failed` Task；其他状态返回 `409 conflict`。
- 系统自动重试仍在原 Task 上增加 `attempt_count`，不调用该接口。
- 管理员手动重试时保留原 Task 不变，创建一个新的 `manual` Run 和一个新 Task。
- 新 Task 复用原任务的订阅、Provider、interval 和时间范围，并以 `retry_of_task_id` 指向原 Task。
- 一次请求只创建一个 Run 和一个 Task；创建成功返回 `202 Accepted`。
- 原订阅或 ProviderInstrument 已失效时返回 `409 conflict`，管理员应先修复配置。

ADM-004 首期冻结以下精确契约：

- 端点需要 `ingestion.manage`；请求体只允许 `reason`，该字段必须非空、去除首尾空白且不超过 512 字符。`requested_by`、actor type 和 Request ID 只从认证上下文注入，客户端不能覆盖。
- 新 Run 使用 `run_type=repair`、`trigger_type=manual`；新 Task 的 `attempt_count=0`、状态为 `pending`，默认 `max_attempts=5`。原 Task 的状态、attempt 和错误记录完全不变。
- 成功返回 `202 Accepted` 及 `run_id/task_id/status/created_at`，只表示新 Run/Task 已原子落库，不调用 Provider，也不等待 Worker。
- Repository 在同一事务中锁定原 Task、校验其状态为 `failed`，并检查订阅启用、ProviderInstrument 有效且支持历史 interval、Provider/Instrument/Asset 当前可用。来源失效返回 `409 CONFLICT`；其他 Task 状态返回 `409 TASK_STATE_CONFLICT`；未知 Task 返回 `404 TASK_NOT_FOUND`。
- 同一原失败 Task 只能有一个 `pending/running/retry_wait` 后继；重复或并发请求由原 Task 行锁和 `uq_active_manual_retry` 共同保护，返回 `409 MANUAL_RETRY_ALREADY_RUNNING`。后继进入终态后可以再次显式重试，保留完整链路。

### 5.4 取消任务

```http
POST /api/market-info/v1/ingestion-tasks/{task_id}/cancel
```

```json
{
  "reason": "incorrect time range"
}
```

- `pending` 和 `retry_wait` 可以直接变为 `canceled`。
- `running` 采用协作式取消：Service 将状态改为 `canceled`，Worker 在最终事务中发现任务已取消后放弃行情、checkpoint 和质量问题写入。
- `success`、`failed` 和 `canceled` 是终态，不再接受取消，返回 `409 conflict`。
- 取消成功返回最新 Task 状态；接口不等待 Worker 当前的外部 API 调用退出。
- Service 取消和 Worker 最终提交都必须锁定并检查同一行 Task，避免取消与提交竞态造成数据污染。

ADM-004 首期冻结以下精确契约：

- 端点需要 `ingestion.manage`，请求体与审计注入规则和 retry 相同。
- 成功返回 `200 OK` 及 `task_id/run_id/status/canceled_at`；`status` 固定为已提交的 `canceled`。未知 Task 返回 `404 TASK_NOT_FOUND`，终态或竞态冲突返回 `409 TASK_STATE_CONFLICT`。
- Task 状态变更、`canceled_by/cancel_reason` 和父 Run `context.operations` 中的 actor type、Request ID、reason、task ID、发生时间在同一短事务持久化。该内部审计数组不作为管理查询的公开响应字段。
- 事务提交后 Service 调用统一 RunService 刷新查询缓存；刷新失败不能回滚已经提交的取消，也不能诱导客户端重复写。Run/Task 查询仍根据 Task 真值返回当前状态。
- retry/cancel 首期均不新增 migration，继续使用既有任务行、Run JSON context 与 `uq_active_manual_retry`。

所有手动重试和取消操作都需要管理员权限，并记录 `requested_by`、`reason` 和审计上下文。

## 6. 健康检查 API

首期提供两个不带业务版本前缀的基础健康检查端点。

### 6.1 存活检查

```http
GET /healthz
```

只判断服务进程是否存活，不访问 PostgreSQL、Longbridge 或 Bybit。正常固定返回 `200 OK`：

```json
{
  "status": "ok"
}
```

该端点供 Docker 或进程管理器判断是否需要重启服务，不得因为外部依赖短暂不可用而失败。

### 6.2 就绪检查

```http
GET /readyz
```

首期检查 PostgreSQL 能否在短超时时间内连接、数据库 schema/migration 版本是否兼容，以及服务启动初始化是否完成。成功返回 `200 OK`：

```json
{
  "status": "ready"
}
```

失败返回 `503 Service Unavailable`：

```json
{
  "status": "not_ready",
  "reason": "database_unavailable"
}
```

约束如下：

- `/readyz` 不调用任何外部行情 Provider。
- Provider 故障、行情延迟或单个采集任务失败不影响 API 就绪状态，相关状态通过 `/api/market-info/v1/providers/status` 查看。
- Scheduler 或 Worker 的业务失败不影响 API 就绪状态。
- `/healthz` 和 `/readyz` 不要求登录，但不得返回数据库地址、凭证、堆栈或底层错误详情。
- 所有依赖检查必须使用短超时，避免健康检查请求自身堆积。

首期不提供重复的 `/api/market-info/v1/health` 汇总接口。未来多个 Go 服务采用相同运行规范后，可将健康检查路由、超时控制、响应结构和 migration 兼容性检查抽取为 Go 服务底层库；在接口稳定前不提前拆分公共组件。

## 7. 前端采集任务管理页面

为了方便观察和运维，前端应增加“市场数据采集”管理页面。该页面属于市场资讯服务的管理能力，不属于交易服务。

首期建议包含四块能力：

| 区域 | 主要功能 |
| --- | --- |
| 数据源状态 | 查看 longbridge、bybit 的启用状态、最近成功时间、失败次数、延迟和健康状态 |
| 采集订阅 | 查看和设置 `collection_subscriptions`，包括 provider instrument、周期、启停、优先级和延迟参数 |
| 任务运行 | 查看 `ingestion_runs` 和 `ingestion_tasks`，支持按状态、provider、instrument、interval、时间范围筛选 |
| 手动操作 | 发起单范围历史回填、重试失败任务、取消活动任务 |

页面只应调用市场资讯服务提供的管理 API，不直接访问数据库。

UI-001 已把“数据源状态”接入仓库现有 XR-Trading Web 页面；UI-002 在同一页面增加“采集订阅”二级页签。浏览器只使用 XR-Trading 登录会话访问同源路径，Python Web 入口再访问市场资讯服务：

```text
Browser
  -> XR-Trading Web 精确的 Provider 状态或订阅路由
  -> Market Information Service 同名 API
```

代理先验证 XR-Trading 登录会话，再从服务端环境读取 `MARKET_INFO_SERVICE_URL`（本地默认 `http://127.0.0.1:8090`）。只读请求使用 `MARKET_INFO_READ_BEARER_TOKEN`，管理写请求使用 `MARKET_INFO_MANAGE_BEARER_TOKEN`；首期 Go 服务仍使用单个本地管理 Token 时，后者可暂时与只读 Token 使用同一值，后续拆分上游凭据时无需修改浏览器代码。这些 Token 不得发送到浏览器；代理不提供通配转发，只接受文档列出的 Provider、订阅、Run/Task 查询和三条采集操作精确路由。市场资讯服务本地使用 `MARKET_INFO_HTTP_ADDRESS=:8090`，避免与现有 Web 入口的 8080 端口冲突。

所有 XR-Trading 登录用户具有页面侧 `operations.read`；服务端 `XR_TRADING_SUBSCRIPTION_MANAGERS` 逗号分隔白名单授予 `subscriptions.manage`，`XR_TRADING_INGESTION_MANAGERS` 授予 `ingestion.manage`。白名单允许以稳定用户 ID、用户名或邮箱配置并忽略大小写；后一个变量未设置时首期复用订阅管理员名单。BFF 在调用上游前完成权限检查：普通用户可以查看、筛选订阅和任务，但不会获得相应写入口，直接请求写路由返回 403。

Provider 状态面板首期按需加载并允许手工刷新，不自动轮询。页面包含加载、错误重试、空 Provider、Provider 无有效 scope、桌面和移动端展示；状态展示严格区分 configured/health，并把美股 `closed + not_applicable` 显示为“休市/停止计算”。

订阅页面支持 Provider、Instrument code、interval、enabled 筛选和 cursor 续页；创建时显式提供身份、周期、启用状态、优先级、close/revision delay 与 reason，修改时只提交四个可变设置和 reason。revision delay 留空显式发送 `null`。页面按稳定 `error.code` 本地化冲突、校验、权限和服务错误，不提供 DELETE 或 `backfill_from`，也不把订阅启用误解为历史回填或运行中任务取消。

UI-003 在同页增加“任务运行”页签，并以 Runs/Tasks 双视图承载 ADM-003 查询契约。Run 列表支持 type、trigger、聚合状态、requested_by 和创建时间范围，Task 列表支持 run ID、状态、Provider、Instrument、interval 和创建时间范围；两者使用后端 cursor 续页，浏览器把本地时间输入显式转换为 RFC3339 UTC。Run 详情展示 Task 真相汇总及白名单 context，并可带精确 run ID 进入 Task 视图；Task 详情只展示 API 已脱敏的 `error_summary/error_details`，不接触原始错误。

BFF 为 UI-003 开放 Run/Task collection 以及 canonical UUID item 的精确 GET 路由，使用服务端 `MARKET_INFO_READ_BEARER_TOKEN`。UI-004 只额外开放 backfill 和 canonical UUID Task retry/cancel 三条精确 POST 路由，使用 `MARKET_INFO_MANAGE_BEARER_TOKEN`；其他未知路径、方法或通配写请求不会匹配代理。首期页面按需加载并手工刷新，不自动轮询。

UI-004 在“任务运行”中增加“手动回填”视图，并在 Task 详情中按服务端状态机显示操作：`failed` 只可 retry，`pending/running/retry_wait` 只可 cancel，其他终态不显示写按钮。backfill 显式要求 Provider、Instrument code、`1h/1d`、有界历史时间范围和 reason，不支持批量或 `backfill_from`。三类操作提交前均展示页面内二次确认，网络请求期间锁定表单和确认按钮以避免重复点击；backfill/retry 收到 `202` 后按返回的 `run_id` 切换到 Task 视图并打开 `task_id` 详情，cancel 成功后刷新原 Task。页面按稳定 `error.code` 本地化重复活动任务、状态冲突、缺少启用订阅、权限及基础设施错误。

UI-005 在“资产研究”页接入公共行情查询。Asset 选择复用现有资产目录并展示实际发送的 `asset_code`；选择 Asset 后读取 `/instruments`，把后端返回的第一个可用 Instrument、`is_default` Provider 和 `1h` 周期明确展示为默认值。当 Instrument 或 Provider 改变时，下游 Provider/Interval 选项重新计算。最新行情按 Asset 返回多来源卡片，不合并 ProviderInstrument；K 线始终显式发送 `instrument_code`、`provider` 和 `interval`，并支持本地时间转 UTC、排序、limit 和 cursor 续页。

BFF 为 UI-005 只增加 `/instruments`、`/quotes/latest`、`/bars` 三条精确 GET 代理。浏览器仍只携带 XR-Trading 会话，服务端使用只读市场资讯凭据并保留上游错误 envelope 与 Request ID；其他公共行情路径和非 GET 方法不会命中代理。公共查询页面不会调用 Adapter，也不会创建采集任务。

XR-Trading BFF 新增独立 `ingestion.manage` 权限。默认未配置 `XR_TRADING_INGESTION_MANAGERS` 时复用订阅管理员白名单，便于首期部署；显式配置该变量后，采集操作管理员可以与 `subscriptions.manage` 分离。浏览器只根据 `/api/users/me` 返回的 permission 控制入口可见性，BFF 仍在调用上游前执行最终权限检查。

首期管理 API 清单：

```text
GET  /api/market-info/v1/providers/status
GET  /api/market-info/v1/collection-subscriptions
POST /api/market-info/v1/collection-subscriptions
PATCH /api/market-info/v1/collection-subscriptions/{id}

GET  /api/market-info/v1/ingestion-runs
GET  /api/market-info/v1/ingestion-runs/{id}
GET  /api/market-info/v1/ingestion-tasks
GET  /api/market-info/v1/ingestion-tasks/{id}

POST /api/market-info/v1/ingestion-runs/backfill
POST /api/market-info/v1/ingestion-tasks/{id}/retry
POST /api/market-info/v1/ingestion-tasks/{id}/cancel
```

权限边界：

- 公共行情查询、`/healthz` 和 `/readyz` 不要求认证。
- 查看 Provider、订阅和采集任务状态需要 `operations.read`，可以授予普通研究用户。
- 创建或修改订阅需要 `subscriptions.manage`。
- 发起回填、重试和取消任务需要 `ingestion.manage`。
- 缺失或无效 Bearer 凭证返回 `401`；身份有效但缺少对应 permission 返回 `403`。
- 页面不得展示供应商 token、secret、签名材料或账户权限信息。
- 所有手动操作从认证 Principal 写入 `requested_by`，并在 context 中记录 actor type 和 Request ID；reason 由具体请求显式提供。

## 8. 统一错误响应与错误码

成功响应保持各 API 定义的直接结构；错误响应统一使用 `error` envelope：

```json
{
  "error": {
    "code": "TASK_STATE_CONFLICT",
    "message": "task cannot be canceled in its current state",
    "retryable": false,
    "details": {
      "task_id": "019...",
      "current_status": "success"
    },
    "request_id": "req_019..."
  }
}
```

字段约束：

- `code` 是稳定的机器可读编码，前端根据该字段判断错误类型并展示本地化文案。
- `message` 用于开发与排障，不得作为调用方业务判断条件。
- `retryable` 表示调用方是否适合稍后重试同一请求，不代表服务会自动重试。
- `details` 只包含安全的结构化信息，不得返回 SQL、堆栈、内部连接地址或 Provider 凭证。
- 每个 API 响应都返回 `X-Request-ID` header；调用方传入规范的 `req_<UUIDv7>` 时可以沿用，否则由服务生成。
- 错误响应中的 `request_id` 与 header 一致，并写入结构化日志，以串联 API、Service 和任务排障信息。

首期 HTTP 状态与典型业务错误码：

| HTTP 状态 | 典型错误码 |
| --- | --- |
| `400 Bad Request` | `INVALID_ARGUMENT`、`INVALID_TIME_RANGE`、`UNSUPPORTED_INTERVAL` |
| `401 Unauthorized` | `UNAUTHENTICATED` |
| `403 Forbidden` | `PERMISSION_DENIED` |
| `404 Not Found` | `ASSET_NOT_FOUND`、`INSTRUMENT_NOT_FOUND`、`SUBSCRIPTION_NOT_FOUND`、`TASK_NOT_FOUND` |
| `409 Conflict` | `SUBSCRIPTION_ALREADY_EXISTS`、`TASK_STATE_CONFLICT`、`BACKFILL_ALREADY_RUNNING`、`MANUAL_RETRY_ALREADY_RUNNING` |
| `429 Too Many Requests` | `RATE_LIMITED` |
| `500 Internal Server Error` | `INTERNAL_ERROR` |
| `503 Service Unavailable` | `SERVICE_UNAVAILABLE`、`DATABASE_UNAVAILABLE` |

请求字段校验失败时，使用 `INVALID_ARGUMENT` 并在 `details.fields` 中一次返回所有已发现的问题：

```json
{
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "request validation failed",
    "retryable": false,
    "details": {
      "fields": [
        {
          "field": "start_time",
          "reason": "must be earlier than end_time"
        }
      ]
    },
    "request_id": "req_019..."
  }
}
```

同一个失败 Task 已存在未结束的手动重试任务时，返回 `409 MANUAL_RETRY_ALREADY_RUNNING`。Service 层先检查状态，数据库部分唯一索引作为并发竞态下的最终保护；首期不额外引入完整的 `Idempotency-Key` 机制。
