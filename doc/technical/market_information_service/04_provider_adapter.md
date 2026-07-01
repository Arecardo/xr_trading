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
    ProviderCode() string

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
    ProviderCode string
    Markets      []ProviderMarketCapability
}

type ProviderMarketCapability struct {
    ProviderMarket    string
    AssetTypes        []AssetType
    InstrumentTypes   []InstrumentType
    SupportsQuote     bool
    SupportsBars      bool
    Intervals         []BarInterval
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
    ProviderInstrumentID uuid.UUID
    InstrumentID         uuid.UUID
    AssetID              uuid.UUID

    ProviderCode   string
    ProviderMarket string
    ExternalSymbol string

    InstrumentCode string
    InstrumentSymbol string
    QuoteCurrency string

    Metadata map[string]any
}
```

设计原因：

- `ProviderInstrumentID` 用于追溯供应商映射。
- `InstrumentID` 用于写入行情主表。
- `AssetID` 只作为辅助上下文，不作为行情唯一身份。
- `ExternalSymbol` 是适配器真正调用外部 API 的代码。
- `InstrumentCode`、`InstrumentSymbol` 主要用于日志、错误信息和排查。

### 8.5 最新行情数据格式

```go
type ProviderQuote struct {
    ProviderInstrumentID uuid.UUID
    InstrumentID         uuid.UUID
    AssetID              uuid.UUID
    ProviderCode         string
    Source               string

    Price       decimal.Decimal
    BidPrice    *decimal.Decimal
    AskPrice    *decimal.Decimal
    BidSize     *decimal.Decimal
    AskSize     *decimal.Decimal
    Volume24h   *decimal.Decimal
    Turnover24h *decimal.Decimal

    QuoteTime    time.Time
    ReceivedTime time.Time

    RawPayload json.RawMessage
}
```

约束：

- `ProviderCode` 必须等于供应商编码，例如 `longbridge`、`bybit`。
- `Source` 表示具体行情来源，第一阶段可与 `ProviderCode` 相同；未来引入聚合源、复权序列或同供应商多数据集时可以进一步细分。
- `QuoteTime` 表示供应商行情时间。
- `ReceivedTime` 表示服务收到数据的时间。
- `Price` 不得为空。
- 可选字段使用指针，避免把“供应商未提供”误写成 0。
- 最新行情写入时不得覆盖其他 `source` 的价格。

### 8.6 K 线请求与返回格式

```go
type FetchBarsRequest struct {
    Instrument ProviderInstrumentRef
    Interval   BarInterval
    StartTime  time.Time
    EndTime    time.Time
    Limit      int
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

```go
type ProviderBar struct {
    ProviderInstrumentID uuid.UUID
    InstrumentID         uuid.UUID
    AssetID              uuid.UUID
    ProviderCode         string
    Source               string

    Interval  BarInterval
    OpenTime  time.Time
    CloseTime time.Time

    Open    decimal.Decimal
    High    decimal.Decimal
    Low     decimal.Decimal
    Close   decimal.Decimal
    Volume  decimal.Decimal
    Turnover *decimal.Decimal

    IsClosed bool

    RawPayload json.RawMessage
}
```

约束：

- `ProviderCode` 必须等于供应商编码，例如 `longbridge`、`bybit`。
- `Source` 表示具体行情来源，第一阶段可与 `ProviderCode` 相同。
- `OpenTime` 和 `CloseTime` 统一使用 UTC。
- `OpenTime` 是 K 线主时间，后续写入 `market_bars.open_time`。
- `IsClosed` 必须明确，避免把未闭合 K 线当成正式历史数据。
- `Turnover` 可为空，因为不同供应商对成交额支持不同。
- `RawPayload` 用于排查和追溯，应在写日志和存储前过滤敏感字段。

### 8.7 枚举类型

```go
type BarInterval string

const (
    BarInterval1h BarInterval = "1h"
    BarInterval1d BarInterval = "1d"
)

type AssetType string

const (
    AssetTypeStock  AssetType = "STOCK"
    AssetTypeETF    AssetType = "ETF"
    AssetTypeCrypto AssetType = "CRYPTO"
)

type InstrumentType string

const (
    InstrumentTypeEquity InstrumentType = "EQUITY"
    InstrumentTypeETF    InstrumentType = "ETF"
    InstrumentTypeSpot   InstrumentType = "SPOT"
)
```

后续新增分钟线、周线、永续合约或期权时，只扩展枚举和能力声明，不改变第一阶段接口语义。

### 8.8 错误分类

适配器应把供应商错误转换成统一错误类型，供采集层决定重试、跳过或标记配置问题。

```go
type ProviderError struct {
    ProviderCode string
    Code         ProviderErrorCode
    Message      string
    RetryAfter   *time.Duration
    Cause        error
}

type ProviderErrorCode string

const (
    ProviderErrorRateLimited        ProviderErrorCode = "rate_limited"
    ProviderErrorTemporaryUnavailable ProviderErrorCode = "temporary_unavailable"
    ProviderErrorUnauthorized       ProviderErrorCode = "unauthorized"
    ProviderErrorInvalidInstrument  ProviderErrorCode = "invalid_instrument"
    ProviderErrorUnsupportedInterval ProviderErrorCode = "unsupported_interval"
    ProviderErrorBadRequest         ProviderErrorCode = "bad_request"
    ProviderErrorUnknown            ProviderErrorCode = "unknown"
)
```

建议处理规则：

| 错误类型 | 采集层动作 |
| --- | --- |
| `rate_limited` | 按 `RetryAfter` 或退避策略重试 |
| `temporary_unavailable` | 可重试，超过阈值后记录任务失败 |
| `unauthorized` | 不重试，标记供应商配置异常 |
| `invalid_instrument` | 不重试，标记供应商品种映射异常 |
| `unsupported_interval` | 不重试，标记订阅配置异常 |
| `bad_request` | 不重试，记录请求构造或配置问题 |
| `unknown` | 有限重试，保留原始错误供排查 |

### 8.9 Registry 与选择逻辑

适配器注册表按 `provider_code` 选择具体实现：

```go
type AdapterRegistry interface {
    Get(providerCode string) (MarketDataAdapter, bool)
    List() []MarketDataAdapter
}
```

数据源优先级不由适配器决定。对于同一个 `Instrument` 存在多个 `ProviderInstrument` 的情况，由采集配置和查询层根据 `provider_instruments.priority`、`source` 和使用场景明确选择。
