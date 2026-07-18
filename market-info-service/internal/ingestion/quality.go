package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// StructuralBarQualityValidator enforces deterministic storage invariants.
// Closed bars are valid; still-open bars are retained as warning snapshots and
// never advance the closed-bar checkpoint.
type StructuralBarQualityValidator struct{}

// NewStructuralBarQualityValidator constructs the first-phase structural
// validator. Rule-based issue generation can be added behind the same port.
func NewStructuralBarQualityValidator() *StructuralBarQualityValidator {
	return &StructuralBarQualityValidator{}
}

// ValidateBars maps provider DTOs into domain bars and assigns their initial
// quality status. It does not infer calendar gaps without a trading calendar.
func (*StructuralBarQualityValidator) ValidateBars(ctx context.Context, execution ExecutionContext, providerBars []ports.ProviderBar) (QualityResult, error) {
	if ctx == nil {
		return QualityResult{}, fmt.Errorf("validate bars: %w", domain.ErrInvalidData)
	}
	reference, err := execution.Reference()
	if err != nil {
		return QualityResult{}, fmt.Errorf("validate bars context: %w", err)
	}
	bars := make([]domain.MarketBar, 0, len(providerBars))
	for index, providerBar := range providerBars {
		if err := ctx.Err(); err != nil {
			return QualityResult{}, err
		}
		if err := providerBar.ValidateFor(reference); err != nil {
			return QualityResult{}, fmt.Errorf("validate provider bar %d: %w", index, err)
		}
		metadata, err := rawPayloadMetadata(providerBar.RawPayload)
		if err != nil {
			return QualityResult{}, fmt.Errorf("build provider bar metadata %d: %w", index, err)
		}
		quality := domain.QualityStatusValid
		if !providerBar.IsClosed {
			quality = domain.QualityStatusWarning
		}
		bar, err := domain.NewBar(domain.MarketBar{
			InstrumentID: providerBar.InstrumentID, ProviderInstrumentID: providerBar.ProviderInstrumentID,
			Interval: providerBar.Interval, OpenTime: providerBar.OpenTime, CloseTime: providerBar.CloseTime,
			OpenPrice: providerBar.Open, HighPrice: providerBar.High, LowPrice: providerBar.Low, ClosePrice: providerBar.Close,
			BaseVolume: providerBar.BaseVolume, QuoteVolume: providerBar.QuoteVolume, TradeCount: providerBar.TradeCount,
			IsClosed: providerBar.IsClosed, QualityStatus: quality, ProviderUpdatedAt: providerBar.ProviderUpdatedAt,
			CollectedAt: providerBar.ReceivedAt, RawHash: normalizedBarHash(providerBar), Metadata: metadata,
		})
		if err != nil {
			return QualityResult{}, fmt.Errorf("normalize provider bar %d: %w", index, err)
		}
		bars = append(bars, bar)
	}
	return QualityResult{Bars: bars, Issues: []domain.DataQualityIssue{}}, nil
}

func rawPayloadMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("provider payload is invalid JSON")
	}
	return json.Marshal(struct {
		ProviderPayload json.RawMessage `json:"provider_payload"`
	}{ProviderPayload: append(json.RawMessage(nil), raw...)})
}

func normalizedBarHash(bar ports.ProviderBar) string {
	write := func(hash interface{ Write([]byte) (int, error) }, value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	hash := sha256.New()
	write(hash, bar.OpenTime.Time().Format("2006-01-02T15:04:05.999999999Z07:00"))
	write(hash, bar.CloseTime.Time().Format("2006-01-02T15:04:05.999999999Z07:00"))
	write(hash, bar.Open.String())
	write(hash, bar.High.String())
	write(hash, bar.Low.String())
	write(hash, bar.Close.String())
	write(hash, optionalDecimalText(bar.BaseVolume))
	write(hash, optionalDecimalText(bar.QuoteVolume))
	if bar.TradeCount != nil {
		write(hash, fmt.Sprintf("%d", *bar.TradeCount))
	} else {
		write(hash, "")
	}
	write(hash, fmt.Sprintf("%t", bar.IsClosed))
	return hex.EncodeToString(hash.Sum(nil))
}

func optionalDecimalText(value *domain.Decimal) string {
	if value == nil {
		return ""
	}
	return value.String()
}
