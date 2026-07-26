# Provider 适配器设计

> 来源：拆分自 `../10_market_information_service.md`。

## 8. Provider 适配器接口设计

Provider 适配器是市场资讯服务连接外部行情供应商的基础设施组件。采集层通过统一接口调用适配器，具体的长桥、Bybit 或未来数据商实现放在 `internal/providers` 下。

适配器的核心定位：

- 负责供应商认证、HTTP/WebSocket 客户端封装、请求构造、分页、限流感知和错误分类。
- 负责把供应商字段转换成服务内部统一数据格式。
- 负责保留供应商原始响应中的必要追溯信息。
- 不负责生成采集任务、数据库写入、资产归并、跨源价格选择、策略信号或交易账户数据。

### 8.1 推荐目录位置

```text
internal/
  ingestion/
    ports/
      market_data_adapter.go
    service.go
  providers/
    registry.go
    longbridge/
      adapter.go
      client.go
      mapper.go
      errors.go
    bybit/
      adapter.go
      client.go
      mapper.go
      errors.go
```

依赖方向为：

```text
ingestion -> ports.MarketDataAdapter
providers/longbridge -> implements ports.MarketDataAdapter
providers/bybit -> implements ports.MarketDataAdapter
```

采集层只依赖接口，不直接依赖某个供应商 SDK 或 HTTP Client。

### 8.2 适配器接口

第一阶段保持接口克制，仅覆盖最新行情和历史 K 线：

```go
type MarketDataAdapter interface {
    ProviderCode() domain.Code

    Capabilities(ctx context.Context) (ProviderCapabilities, error)

    FetchLatestQuotes(
        ctx context.Context,
        instruments []ProviderInstrumentRef,
    ) ([]ProviderQuote, error)

    FetchBars(
        ctx context.Context,
        req FetchBarsRequest,
    ) (FetchBarsResult, error)
}
```

说明：

- `ProviderCode` 返回稳定供应商编码，例如 `longbridge`、`bybit`。
- `Capabilities` 返回适配器支持能力，用于启动检查、配置校验和任务生成保护。
- `FetchLatestQuotes` 支持批量获取最新行情。
- `FetchBars` 获取某个供应商品种在指定时间范围内的 K 线。
- 接口不暴露供应商原始请求参数；供应商差异由具体 adapter 内部处理。

### 8.3 能力声明

```go
type ProviderCapabilities struct {
    ProviderCode domain.Code
    Markets      []ProviderMarketCapability
}

type ProviderMarketCapability struct {
    ProviderMarket    string
    AssetTypes        []domain.AssetType
    InstrumentTypes   []domain.InstrumentType
    SupportsQuote     bool
    SupportsBars      bool
    Intervals         []domain.BarInterval
    MaxBatchSize      int
    MaxBarsPerRequest int
}
```

第一阶段至少需要表达：

- 是否支持最新行情。
- 是否支持历史 K 线。
- 支持的市场类型，例如美股、ETF、加密现货。
- 支持的周期，例如 `1h`、`1d`。
- 批量 quote 的最大数量。
- 单次 K 线请求最大返回条数。

供应商真实限制以实现阶段查阅官方文档和 SDK 后为准，当前接口只定义表达方式。

### 8.4 供应商品种引用

采集层调用适配器时，不直接传 `Asset`，而是传 `ProviderInstrumentRef`。

```go
type ProviderInstrumentRef struct {
    ProviderInstrumentID   domain.ID
    ProviderInstrumentCode domain.Code
    InstrumentID           domain.ID
    AssetID                domain.ID

    ProviderCode    domain.Code
    ProviderMarket  string
    AssetType       domain.AssetType
    InstrumentType  domain.InstrumentType
    ExternalSymbol string

    InstrumentCode domain.Code
    InstrumentSymbol string
    QuoteCurrency string

    Metadata json.RawMessage
}
```

设计原因：

- `ProviderInstrumentID` 用于追溯供应商映射。
- `ProviderInstrumentCode` 是同一映射的稳定可读编码，用于日志和排查，但数据库关联仍使用 UUID。
- `InstrumentID` 用于写入行情主表。
- `AssetID` 只作为辅助上下文，不作为行情唯一身份。
- `AssetType` 和 `InstrumentType` 使用领域枚举，使 Registry 能按 adapter 声明的市场能力校验该映射，不根据 symbol 或 provider 名称猜测类型。
- `ExternalSymbol` 是适配器真正调用外部 API 的代码。
- `InstrumentCode`、`InstrumentSymbol` 主要用于日志、错误信息和排查。

### 8.5 最新行情数据格式

```go
type ProviderQuote struct {
    ProviderInstrumentID domain.ID
    InstrumentID         domain.ID
    AssetID              domain.ID
    ProviderCode         domain.Code

    LastPrice      domain.Decimal
    BidPrice       *domain.Decimal
    AskPrice       *domain.Decimal
    BidSize        *domain.Decimal
    AskSize        *domain.Decimal
    Open24H        *domain.Decimal
    High24H        *domain.Decimal
    Low24H         *domain.Decimal
    BaseVolume24H  *domain.Decimal
    QuoteVolume24H *domain.Decimal

    MarketTime domain.UTCInstant
    ReceivedAt domain.UTCInstant

    RawPayload json.RawMessage
}
```

约束：

- `ProviderCode` 必须等于供应商编码，例如 `longbridge`、`bybit`。
- `ProviderInstrumentID` 是精确、持久化的来源身份；不再额外传递可能漂移或与映射冲突的自由文本 `Source`。查询时通过 ProviderInstrument 关联 Provider 得到展示来源。
- `MarketTime` 表示供应商行情时间。
- `ReceivedAt` 表示服务收到数据的时间。
- `LastPrice` 必须存在且不得为负。
- 可选字段使用指针，避免把“供应商未提供”误写成 0。
- 最新行情写入时不得覆盖其他 `ProviderInstrumentID` 的价格。

### 8.6 K 线请求与返回格式

```go
type FetchBarsRequest struct {
    Instrument ProviderInstrumentRef
    Interval   domain.BarInterval
    StartTime  domain.UTCInstant
    EndTime    domain.UTCInstant
    Limit      int
    Cursor     string
}

type FetchBarsResult struct {
    Bars       []ProviderBar
    NextCursor string
    HasMore    bool
}
```

`StartTime` 与 `EndTime` 使用左闭右开区间：

```text
[StartTime, EndTime)
```

适配器返回的 K 线必须按 `OpenTime` 升序排列。

`Cursor` 是供应商不透明分页位置：第一页为空；当 `HasMore = true` 时，调用方必须把 `NextCursor` 原样放入下一次请求。下一页的 Instrument、Interval、时间范围和 Limit 保持不变。

```go
type ProviderBar struct {
    ProviderInstrumentID domain.ID
    InstrumentID         domain.ID
    AssetID              domain.ID
    ProviderCode         domain.Code

    Interval  domain.BarInterval
    OpenTime  domain.UTCInstant
    CloseTime domain.UTCInstant

    Open        domain.Decimal
    High        domain.Decimal
    Low         domain.Decimal
    Close       domain.Decimal
    BaseVolume  *domain.Decimal
    QuoteVolume *domain.Decimal
    TradeCount  *int64

    IsClosed bool
    ProviderUpdatedAt *domain.UTCInstant
    ReceivedAt domain.UTCInstant

    RawPayload json.RawMessage
}
```

约束：

- `ProviderCode` 必须等于供应商编码，例如 `longbridge`、`bybit`。
- `ProviderInstrumentID` 是精确来源身份，不以 Provider code 或 Asset ID 代替。
- `OpenTime` 和 `CloseTime` 统一使用 UTC。
- `OpenTime` 是 K 线主时间，后续写入 `market_bars.open_time`。
- `IsClosed` 必须明确，避免把未闭合 K 线当成正式历史数据。
- 成交量、成交额和成交笔数均可为空，因为不同供应商支持程度不同；缺失值不得转换为 0。
- `RawPayload` 用于排查和追溯，应在写日志和存储前过滤敏感字段。

### 8.7 枚举与值对象

适配器端口不重复定义 UUID、可读编码、decimal、UTC、AssetType、InstrumentType 或 BarInterval，而是复用 `internal/domain` 中已经冻结并通过严格构造校验的值对象。这样 Adapter 输出、Application Service 和 Repository 不会出现同义但不可直接赋值的第二套类型。

后续新增分钟线、周线、永续合约或期权时，只扩展枚举和能力声明，不改变第一阶段接口语义。

### 8.8 错误分类

适配器应把供应商错误转换成统一错误类型，供采集层决定重试、跳过或标记配置问题。

```go
type ProviderError struct {
    ProviderCode domain.Code
    Code         ProviderErrorCode
    Message      string
    RetryAfter   *time.Duration
    Cause        error
}

type ProviderErrorCode string

const (
    ProviderErrorRateLimited        ProviderErrorCode = "rate_limited"
    ProviderErrorNetwork            ProviderErrorCode = "network"
    ProviderErrorTemporaryUnavailable ProviderErrorCode = "temporary_unavailable"
    ProviderErrorUnauthorized       ProviderErrorCode = "unauthorized"
    ProviderErrorInvalidInstrument  ProviderErrorCode = "invalid_instrument"
    ProviderErrorUnsupportedInterval ProviderErrorCode = "unsupported_interval"
    ProviderErrorBadRequest         ProviderErrorCode = "bad_request"
    ProviderErrorInvalidResponse    ProviderErrorCode = "invalid_response"
    ProviderErrorUnknown            ProviderErrorCode = "unknown"
)
```

建议处理规则：

| 错误类型 | 采集层动作 |
| --- | --- |
| `rate_limited` | 按 `RetryAfter` 或退避策略重试 |
| `network` | 网络中断、超时等瞬时错误，按退避策略有限重试 |
| `temporary_unavailable` | 可重试，超过阈值后记录任务失败 |
| `unauthorized` | 不重试，标记供应商配置异常 |
| `invalid_instrument` | 不重试，标记供应商品种映射异常 |
| `unsupported_interval` | 不重试，标记订阅配置异常 |
| `bad_request` | 不重试，记录请求构造或配置问题 |
| `invalid_response` | 不重试，不写入行情，记录供应商响应映射问题 |
| `unknown` | 有限重试，保留原始错误供排查 |

`ProviderError.Message` 只允许保存经过适配器明确构造的安全摘要；`Cause` 支持 `errors.Is/As` 排查，但 `Error()` 不拼接 Cause 文本，避免供应商响应、密钥或签名进入任务错误和普通日志。`retryable` 由错误码确定，不允许通过字符串匹配推断；所有可重试错误仍受 Task `max_attempts` 限制。

### 8.9 DTO 契约校验

- Capabilities 拒绝重复市场、重复枚举、矛盾的 operation/limit 声明和不支持的 interval。
- Quote 批量结果只能包含本次请求的 ProviderInstrument，同一来源不得重复；供应商没有当前快照时允许省略对应结果。
- K 线结果数量不得超过 Limit，必须严格按 OpenTime 升序，并且每条记录属于请求的 ProviderInstrument、interval 和 `[start,end)`。
- `HasMore = true` 时必须返回非空、与当前 cursor 不同的 `NextCursor` 和至少一条数据；终页不得携带 NextCursor，防止 Worker 无限分页。
- Adapter 输出进入质量校验或 Repository 之前必须通过这些 DTO 契约；任何失败归类为 `invalid_response`，不得部分落库。

### 8.10 Registry 与选择逻辑

适配器注册表按 `provider_code` 选择具体实现：

```go
type AdapterRegistry interface {
    Get(providerCode domain.Code) (MarketDataAdapter, bool)
    List() []MarketDataAdapter
    Capabilities(providerCode domain.Code) (ProviderCapabilities, bool)
    ValidateLatestQuoteRequest(
        providerCode domain.Code,
        instruments []ProviderInstrumentRef,
    ) error
    ValidateBarsRequest(req FetchBarsRequest) error
}
```

Registry 使用不可变运行时快照：

1. 服务启动时为每个 adapter 调用一次 `Capabilities(ctx)`，校验 adapter 的 `ProviderCode()` 与能力声明一致。
2. 同一 `provider_code` 重复注册、空 adapter、无效 capability 或 provider code 不一致时启动失败，不允许后注册覆盖先注册。
3. 注册成功后深拷贝并缓存 capability；运行期 `Get`、`List`、`Capabilities` 和请求校验只读取快照，不重复访问供应商。
4. `List` 按 `provider_code` 稳定排序；`List` 和 `Capabilities` 返回副本，调用方不能修改 Registry 内部状态。
5. Registry 构造完成后不提供动态注册和删除，因此无需在热路径加锁，并允许 Scheduler、Worker 和 IngestionService 并发读取。动态重载留到后续确有需求时再设计。

请求校验规则：

- Quote 与 K 线请求先通过 DTO 自身校验，再按 `provider_market + asset_type + instrument_type` 匹配 capability。
- Quote 必须声明 `SupportsQuote`，同一请求不得包含重复 ProviderInstrument，且数量不得超过对应市场的 `MaxBatchSize`。
- K 线必须声明 `SupportsBars`，interval 必须在 `Intervals` 中，Limit 不得超过 `MaxBarsPerRequest`。
- 未注册 adapter、能力不支持和请求超过供应商上限分别返回稳定 sentinel error，调用方不得匹配错误字符串。
- 这些校验发生在调用外部 API 之前；任务运行期遇到不支持的配置应按不可重试失败处理。

数据源优先级不由适配器决定。对于同一个 `Instrument` 存在多个 `ProviderInstrument` 的情况，由采集配置和查询层根据 `provider_instruments.priority`、精确 ProviderInstrument 身份和使用场景明确选择。

### 8.11 Bybit Spot Adapter 第一阶段实现

第一阶段直接使用 Bybit V5 公共市场 REST API，不引入供应商 SDK，也不配置 API Key：

| 能力 | Bybit API | 适配器行为 |
| --- | --- | --- |
| 最新行情 | `GET /v5/market/tickers?category=spot` | 单个品种携带 `symbol`；多个品种只请求一次全量 Spot ticker 并按 ExternalSymbol 精确过滤 |
| K 线 | `GET /v5/market/kline?category=spot` | 强制携带 symbol、interval、start、end、limit；将供应商倒序结果转为升序 DTO |

官方契约参考：[Get Tickers](https://bybit-exchange.github.io/docs/v5/market/tickers)、[Get Kline](https://bybit-exchange.github.io/docs/v5/market/kline)、[Rate Limit](https://bybit-exchange.github.io/docs/v5/rate-limit) 和 [Error Codes](https://bybit-exchange.github.io/docs/v5/error)。

首期 capability 固定为：

- `provider_market = spot`、`asset_type = CRYPTO`、`instrument_type = SPOT`。
- Quote adapter 批量上限为 100 个映射；这是服务自身的防御性上限。多个 symbol 仍只发起一次不带 symbol 的公开 ticker 请求。
- K 线支持 `1h -> 60`、`1d -> D`，单次最大 1000 条。
- Bybit symbol 必须是大写字母和数字组成的现货代码，例如 `BTCUSDT`；Worker 只传 ProviderInstrument 的 ExternalSymbol，不自行拼接交易对。

Ticker 映射：

| Bybit 字段 | ProviderQuote 字段 |
| --- | --- |
| `lastPrice` | `LastPrice` |
| `bid1Price` / `bid1Size` | `BidPrice` / `BidSize` |
| `ask1Price` / `ask1Size` | `AskPrice` / `AskSize` |
| `prevPrice24h` | `Open24H`，表示滚动 24 小时窗口起点价格 |
| `highPrice24h` / `lowPrice24h` | `High24H` / `Low24H` |
| `volume24h` / `turnover24h` | `BaseVolume24H` / `QuoteVolume24H` |
| 响应顶层 `time` | `MarketTime` |

K 线 tuple 按官方顺序映射 `startTime/open/high/low/close/volume/turnover`。`CloseTime` 由 OpenTime 加 interval 得到；只有供应商响应时间与本地接收时间都不早于 CloseTime 时才标记 `IsClosed = true`，避免时钟偏差把未闭合 K 线写成历史正式数据。

Bybit K 线接口没有原生 cursor，且返回离 `end` 最近的数据。Adapter 使用带版本的 opaque cursor 保存“下一页排他的 end 毫秒时间戳”：

1. 首次请求把领域 `[StartTime, EndTime)` 转换为 Bybit 的毫秒参数，`end` 取严格小于 EndTime 的最后一个毫秒时间点。
2. 每页先按 OpenTime 升序返回；满页且最早一根仍晚于 StartTime 时，NextCursor 记录该最早 OpenTime。
3. 下一页用 cursor 对应时间减 1ms 作为 Bybit `end`，向更早时间翻页。
4. cursor 带版本前缀并校验必须严格位于请求范围内；调用方不得解析或构造 cursor。

错误处理：

- HTTP 429、`retCode = 10006/429` 归类为 `rate_limited`，优先读取 `Retry-After`，其次读取 `X-Bapi-Limit-Reset-Timestamp`。
- HTTP 403 按 Bybit IP 限流说明使用 10 分钟 RetryAfter；若实际原因是区域限制，仍会受到 Task max_attempts 约束，不会无限重试。
- HTTP 5xx、Bybit timeout/internal error 归类为 `temporary_unavailable`；网络读写错误归类为 `network`。
- API Key 失效、签名或权限类 retCode 归类为 `unauthorized`；虽然公共行情当前不使用密钥，仍保留稳定映射。
- 无效 symbol、请求参数、未知错误和无效响应分别映射为 `invalid_instrument`、`bad_request`、`unknown`、`invalid_response`。
- Provider retMsg、HTTP body 和 transport error 只保存在 Cause，不进入安全 Error 文本；响应体最大读取 4 MiB。

普通测试使用 `httptest` 与脱敏 JSON fixture，不访问外网。真实公共行情 smoke test 必须显式启用：

```bash
BYBIT_SMOKE=1 make smoke-bybit
```

可通过 `BYBIT_BASE_URL` 指向测试网或区域域名；不设置时使用 `https://api.bybit.com`。

### 8.12 Longbridge 美股/ETF Adapter 第一阶段实现

第一阶段使用 Longbridge 官方 Go SDK `github.com/longbridge/openapi-go`，SDK 版本锁定在 `v0.25.2`。SDK 负责凭据认证、长连接协议和 protobuf 编解码；Adapter 在 SDK 外再封装一层窄 `Client` 接口，Worker 及统一端口均看不到 Longbridge 类型。官方契约参考：[Quotes](https://open.longbridge.com/docs/quote/pull/quote)、[Historical Candlesticks](https://open.longbridge.com/docs/quote/pull/history-candlestick)、[Candlestick Definition](https://open.longbridge.com/docs/quote/objects) 和 [官方 Go SDK](https://github.com/longbridge/openapi-go)。

首期 capability 固定为：

- `provider_market = us`。
- 支持 `asset_type = STOCK + instrument_type = EQUITY` 和 `asset_type = ETF + instrument_type = ETF`；Adapter 会再次校验组合，不能交叉搭配。
- 最新行情单次最多 500 个 symbol，沿用官方限制。
- K 线支持 `1h -> PeriodSixtyMinute`、`1d -> PeriodDay`，单次最多 1000 条。
- ExternalSymbol 使用大写 `ticker.region` 格式且首期只接受 `.US`，例如 `AAPL.US`、`SPY.US`；Worker 不自行拼接后缀。
- K 线固定 `AdjustTypeNo`，只请求 `CandlestickTradeSessionNormal`，不混入盘前、盘后和 overnight。

最新行情调用 SDK `Quote(ctx, symbols)`，基础 quote 被明确视为常规交易时段快照：

| Longbridge 字段 | Adapter 行为 |
| --- | --- |
| `last_done` / `timestamp` | 映射为 `LastPrice` / `MarketTime` |
| `open/high/low/volume/turnover` | 属于当日常规交易时段，不是滚动 24 小时；保留在脱敏 `RawPayload`，不得写入 `Open24H/High24H/Low24H/BaseVolume24H/QuoteVolume24H` |
| `prev_close` | 保留在 `RawPayload`，不冒充 `Open24H` |
| `pre_market_quote/post_market_quote/over_night_quote` | 首期不参与最新价格选择，只作为追溯字段保留 |
| bid/ask | Quote API 不提供，统一 DTO 中保持 `nil` |

SDK 返回的是 Go 对象而不是原始响应字节，因此 `RawPayload` 是 Adapter 从公开行情字段生成的稳定、脱敏 JSON 快照，不包含认证信息、协议帧或 SDK 错误文本。Provider 可以省略没有快照的请求品种，但不能返回未请求或重复 symbol；正常输出按请求顺序排列。

历史 K 线使用 `HistoryCandlesticksByOffset(..., isForward=false)` 向过去分页：

1. 领域请求仍为 `[StartTime, EndTime)`；第一次 offset 使用严格早于 `EndTime` 的时点。
2. `1h` offset 转为 `America/New_York` 本地日期和分钟；`1d` 使用 UTC 日历日期，与 Longbridge 日线 timestamp 的 UTC 零点语义一致。
3. Provider 可能在边界页带回范围外记录，Adapter 过滤后只返回原始领域范围内的 K 线，并统一按 `OpenTime` 升序输出。
4. 满页且最早记录仍晚于 StartTime 时，NextCursor 使用带版本的 `v1:end-sec:` 前缀保存下一页排他的最早 Unix 秒；cursor 必须严格位于原请求范围内。
5. `Open/High/Low/Close/Volume/Turnover/Timestamp` 分别映射为统一 Bar 字段；成交笔数与 ProviderUpdatedAt 保持空值。

常规交易时段 `CloseTime` 规则：

- `1h` 默认是 `OpenTime + 1h`，但不得晚于该交易日美东 16:00，因此 15:30 开始的最后一根按 16:00 结束。
- `1d` 使用 timestamp 的 UTC 日期作为交易日期，并通过共享 `TradingCalendar` 转换为当日真实核心时段 close，自动覆盖普通 DST 和提前收市。
- SDK 没有返回响应时间，`IsClosed` 仅在本地 `ReceivedAt >= CloseTime` 时为真。
- SCH-002 后 Longbridge 与 Scheduler/freshness 复用 `internal/markettime.TradingCalendar`；提前休市日不再从响应猜测 16:00。首期官方日历支持 2026～2028，超出范围明确失败，扩展历史 backfill 前必须先补充经核验的年度日历。

结构化错误映射：

- 业务码 `301606` 映射为 `rate_limited`，首期 RetryAfter 使用 1 秒，最终仍受 Task `max_attempts` 限制。
- `301602` 映射为 `temporary_unavailable`；实现了 `net.Error` 的连接错误映射为 `network`。
- `301604` 无行情权限映射为 `unauthorized`；历史接口的 `301607` 月度品种额度耗尽也映射为 `unauthorized`，不自动重试。
- Quote 接口的 `301607` 表示请求 symbol 过多，映射为 `bad_request`；本地 500 上限通常会先拦截。
- `301603` 无行情映射为 `invalid_instrument`，`301600` 参数错误映射为 `bad_request`，其他协议错误映射为 `unknown` 并有限重试。
- SDK 的业务 message 和底层连接错误只进入 Cause；安全 Error 文本不拼接这些内容。

Longbridge QuoteContext 持有长连接。生产 bootstrap 通过 `NewFromEnvironment` 或 `NewFromSDKConfig` 创建 Adapter，并必须在服务退出时调用 `Close`；运行期复用同一个 Adapter，不为每个 Task 重建连接。

普通测试使用可注入 fake Client 和脱敏 JSON fixture，不访问外网。真实 smoke test 必须显式配置最小行情权限凭据并启用：

```bash
LONGBRIDGE_SMOKE=1 make smoke-longbridge
```

SDK 从 `LONGBRIDGE_APP_KEY`、`LONGBRIDGE_APP_SECRET`、`LONGBRIDGE_ACCESS_TOKEN` 读取传统凭据，也支持 SDK OAuth 配置。首期部署先使用最小行情权限的 secret 注入，凭据不得写入仓库、fixture、任务错误或普通日志。
