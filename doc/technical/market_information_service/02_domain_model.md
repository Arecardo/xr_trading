# 市场资讯服务领域模型

> 来源：拆分自 `../10_market_information_service.md`。

## 6. 资产与交易品种领域模型

美股、ETF 和加密货币统一采用 `Asset + Instrument + ProviderInstrument` 三层领域模型：

```text
Asset 1 ────── N Instrument 1 ────── N ProviderInstrument
经济资产          具体交易品种              供应商代码映射
```

三个对象分别回答不同问题：

- `Asset`：系统正在研究或配置的经济资产是什么？
- `Instrument`：资产在哪个场所、以什么市场类型和报价币进行交易？
- `ProviderInstrument`：调用某个供应商 API 时使用什么代码和参数？

供应商不是 `Instrument`。多个供应商可以描述同一个交易品种，只是各自使用不同的外部代码。

### 6.1 Asset

`Asset` 表示独立于交易场所和数据供应商的经济资产，是资产研究、组合成员和跨市场归并的基础对象。

建议字段：

| 字段 | 含义 |
| --- | --- |
| `id` | UUIDv7，不可变内部主键 |
| `code` | 唯一可读编码，不作为外键 |
| `asset_type` | `STOCK`、`ETF`、`CRYPTO`、`CASH` |
| `canonical_symbol` | 面向展示和搜索的通用代码，例如 `NVDA`、`BTC` |
| `name` | 资产名称 |
| `status` | `active`、`inactive`、`delisted` |
| `metadata` | 资产类型专属的扩展信息 |
| `created_at` / `updated_at` | 创建和更新时间 |

示例：

```json
{
  "id": "0197c3c7-5f44-7b42-9f17-9d7dcf66d929",
  "code": "asset.crypto.btc",
  "asset_type": "CRYPTO",
  "canonical_symbol": "BTC",
  "name": "Bitcoin",
  "status": "active"
}
```

股票基本面、公司新闻、加密资产级供应信息以及组合研究结果优先关联 `asset_id`。

### 6.2 Instrument

`Instrument` 表示特定交易场所中的具体可交易品种，是行情、交易规则和未来订单路由所关联的对象。

建议字段：

| 字段 | 含义 |
| --- | --- |
| `id` | UUIDv7，不可变内部交易品种主键 |
| `code` | 唯一可读编码，不作为外键 |
| `asset_id` | 所属经济资产的 UUID 外键 |
| `venue` | 交易场所，例如 `NASDAQ`、`BYBIT` |
| `instrument_type` | `EQUITY`、`ETF`、`SPOT`；后续需要时再扩展其他类型 |
| `symbol` | 场所层面的标准交易代码 |
| `quote_asset_id` | 报价资产；加密现货示例为 USDT，可为空并使用 `quote_currency` |
| `quote_currency` | 报价币种，例如 `USD`、`USDT` |
| `market_timezone` | 市场时区 |
| `price_scale` | 价格精度 |
| `quantity_scale` | 数量精度 |
| `lot_size` | 最小数量步长 |
| `min_quantity` | 最小交易数量 |
| `status` | `active`、`suspended`、`delisted` |
| `valid_from` / `valid_to` | 有效时间范围 |
| `metadata` | 市场专属扩展信息 |

加密现货示例：

```json
{
  "id": "0197c3ca-13e0-7b7d-8ff1-c6da977b31e2",
  "code": "instrument.bybit.spot.btc-usdt",
  "asset_id": "0197c3c7-5f44-7b42-9f17-9d7dcf66d929",
  "venue": "BYBIT",
  "instrument_type": "SPOT",
  "symbol": "BTC-USDT",
  "quote_asset_id": "0197c3c8-6c45-7ee1-85bf-b538f45483dc",
  "quote_currency": "USDT",
  "market_timezone": "UTC",
  "status": "active"
}
```

美股示例：

```json
{
  "id": "0197c3cc-4a4e-7c1c-8f13-b60fbdf6af98",
  "code": "instrument.nasdaq.equity.nvda",
  "asset_id": "0197c3cb-7a66-78de-94d8-c6cb18d0714f",
  "venue": "NASDAQ",
  "instrument_type": "EQUITY",
  "symbol": "NVDA",
  "quote_currency": "USD",
  "market_timezone": "America/New_York",
  "status": "active"
}
```

一个 `Asset` 可以关联多个 `Instrument`。例如 BTC 可以同时关联 Bybit `BTC-USDT` 和 Coinbase `BTC-USD`，二者的价格、成交量、流动性和交易规则必须分别保存。

### 6.3 ProviderInstrument

`ProviderInstrument` 表示内部交易品种与外部数据供应商代码之间的映射，仅承担适配和能力描述，不作为组合或行情数据的核心身份。

建议字段：

| 字段 | 含义 |
| --- | --- |
| `id` | UUIDv7，不可变内部映射主键 |
| `code` | 唯一可读编码，不作为外键 |
| `provider_id` | Provider 的 UUID 外键，例如对应 `longbridge`、`bybit` |
| `instrument_id` | 对应内部交易品种的 UUID 外键 |
| `external_symbol` | 供应商要求的代码，例如 `NVDA.US`、`BTCUSDT` |
| `provider_market` | 供应商市场或产品分类，例如 `US`、`spot` |
| `capabilities` | 结构化能力对象，包含 `quote`、`historical` 和支持的 `intervals` |
| `priority` | 同一交易品种存在多个数据源时的选择优先级 |
| `is_default` | 是否为该 Instrument 的默认行情来源 |
| `enabled` | 是否启用采集 |
| `valid_from` / `valid_to` | 映射有效时间范围 |
| `metadata` | 供应商专属参数 |

示例：

```json
{
  "id": "0197c3cd-65b4-7fd5-b554-af0203194988",
  "code": "provider.bybit.spot.btcusdt",
  "provider_id": "0197c3ce-7a19-7795-b76f-8d011cc9b110",
  "instrument_id": "0197c3ca-13e0-7b7d-8ff1-c6da977b31e2",
  "external_symbol": "BTCUSDT",
  "provider_market": "spot",
  "capabilities": {
    "quote": true,
    "historical": true,
    "intervals": ["1h", "1d"]
  },
  "priority": 100,
  "is_default": true,
  "enabled": true
}
```

长桥与其他美股数据商可能使用不同代码描述同一个 NASDAQ NVDA 交易品种：

```text
Instrument: instrument.nasdaq.equity.nvda
  ├── LONGBRIDGE -> NVDA.US
  └── MASSIVE    -> NVDA       （后续阶段）
```

### 6.4 领域约束

- 实体 UUID 创建后不可修改，且不得复用。
- `code` 在同类实体中唯一，统一使用小写点分编码；原则上保持稳定，不作为数据库外键。
- `Instrument` 必须关联一个有效的 `Asset`。
- 现货 Instrument 的 `asset_id` 表示 base asset；`quote_asset_id` 可为空，但存在时不得为零或与 base asset 相同，`quote_currency` 始终必填。
- `price_scale`、`quantity_scale` 范围为 0～18；`lot_size`、`min_quantity` 存在时必须大于零；`market_timezone` 必须是有效 IANA 时区。
- `ProviderInstrument` 必须关联一个有效的 `Instrument`。
- 同一 `provider + external_symbol + provider_market` 在同一有效时间范围内只能映射到一个 `Instrument`。
- 同一 `Instrument` 可以拥有多个供应商映射，但必须明确来源优先级和支持能力。
- `capabilities.intervals` 只接受首期支持的 interval 且不得重复；未知 capability 字段直接拒绝，避免配置拼写错误被静默忽略。
- 默认 ProviderInstrument 必须处于启用状态且 `valid_to` 为空；同一 Instrument 的当前默认来源唯一性由数据库部分唯一索引保证。
- 行情数据必须关联 `instrument_id` 和 `source`，不得只关联 `asset_id`。
- 基本面、新闻或资产级研究数据可关联 `asset_id`；若数据明确针对某个交易场所，则同时关联 `instrument_id`。
- 供应商代码变更通过关闭旧映射并创建新映射处理，不改写历史行情身份。
- 暂停或退市的 Instrument 保留历史记录，不做物理删除。

### 6.5 标识与编码规则

三个实体统一采用双标识：

```text
UUIDv7                     系统身份、主键和外键
code                       配置、API、日志和人工排查使用的可读定位符
```

推荐编码格式：

```text
Asset
  asset.crypto.btc
  asset.equity.us.nvda
  asset.etf.us.spy

Instrument
  instrument.bybit.spot.btc-usdt
  instrument.nasdaq.equity.nvda
  instrument.nyse.etf.spy

ProviderInstrument
  provider.bybit.spot.btcusdt
  provider.longbridge.us.nvda-us
```

具体规则如下：

- UUID 使用 PostgreSQL 原生 `uuid` 类型，不保存为 `varchar`。
- UUIDv7 默认由 Go 服务生成；数据库不依赖某个特定扩展生成主键。
- 所有主键、外键、行情关联和跨服务身份传递均使用 UUID。
- `code` 使用小写点分层级，建立唯一索引，但不参与外键关系。
- `code`、交易场所 `symbol` 和供应商 `external_symbol` 是三个独立字段，禁止混用。
- 配置文件可以使用 `code`，服务加载后必须解析成 UUID 再执行内部逻辑。
- API 响应和领域事件原则上同时返回 UUID 与 `code`，兼顾稳定关联和可观测性。
- `symbol` 或 `external_symbol` 变化时不修改实体 UUID；供应商映射变化通过有效时间范围保留历史。
- `code` 确需调整时，应执行受控变更，并将旧编码写入别名表以保持兼容。

建议使用拆分别名模型：

| 字段 | 含义 |
| --- | --- |
| `id` | UUIDv7 主键 |
| `asset_id` / `instrument_id` | 目标实体 UUID，分别位于 `asset_aliases` 与 `instrument_aliases` |
| `alias_code` | 历史或兼容编码 |
| `valid_from` / `valid_to` | 别名有效时间范围 |
| `created_at` | 创建时间 |

别名只用于解析和迁移兼容，不作为新数据的写入标识。

### 6.6 数据所有权

- `Asset` 和 `Instrument` 属于统一资产目录，是组合、市场资讯和交易服务共享的基础主数据。
- `ProviderInstrument` 由市场资讯服务维护其行情能力和代码映射。
- 交易服务若需要独立的券商下单代码、账户权限或订单能力，应维护交易侧映射，不在行情映射中加入下单职责。
- 市场资讯服务可以缓存资产目录，但不得自行创建与主资产目录冲突的 `Asset` 或 `Instrument`。

现有 `crypto:coinbase:BTC-USD` 一类将场所写入资产 ID 的记录，需要在实施阶段迁移为独立的 `Asset` 与 `Instrument`。旧标识到新 UUID、规范 `code` 和别名的迁移映射将在数据库详细设计中确定。

## 9. 数据一致性与质量原则

- 所有市场时间统一存储为 UTC，同时保留市场时区和日切规则。
- 行情记录不能只使用 `asset_id + price` 表达，必须同时保留 `instrument_id` 和 `provider_instrument_id`。其中 `provider_instrument_id` 是持久化来源身份；API 展示的 `source` 由 Provider 关联生成，不在行情表重复保存自由文本来源。
- 同一资产在不同交易场所或聚合供应商下的价格具有不同语义，必须分别存储，不得互相覆盖。例如 Bybit `BTCUSDT` 成交价属于具体交易场所行情，CoinGecko BTC 价格属于跨市场聚合参考价。
- 交易执行、成交能力和订单风控使用目标交易场所的具体交易品种行情；跨市场研究、市场概览和参考估值可按明确规则使用聚合行情。
- K 线的逻辑来源键为 `instrument_id + provider_instrument_id + interval + open_time`；数据库再增加 `revision` 保存修订历史，并以 `is_current` 标记当前版本。
- 重复采集不得生成重复记录。
- 已闭合与未闭合 K 线必须明确区分。
- 数据修订必须记录来源、采集时间和修订信息。
- OHLC 必须满足 `low <= open/close <= high`，成交量不得为负。
- 明确区分休市、供应商无数据、采集失败和尚未采集。
- 股票公司行动和复权方式必须作为元数据保留。
- 加密成交量的 base/quote 单位必须明确记录。
- 核心价格和数量使用定点十进制，不使用二进制浮点作为持久化标准。
- 行情查询时间范围统一采用 `[start,end)`；`start` 包含、`end` 排除，避免相邻窗口重复边界 K 线。
- `is_closed` 表示市场 K 线是否闭合，`is_current` 表示修订版本是否为当前版本，二者不得混用。
