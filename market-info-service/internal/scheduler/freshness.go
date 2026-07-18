package scheduler

import (
	"errors"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
)

// MarketState is the current regular-session state of a market scope.
type MarketState string

const (
	MarketStateOpen   MarketState = "open"
	MarketStateClosed MarketState = "closed"
)

// FreshnessStatus describes whether the latest closed bar has caught up with
// the latest bar that should be available.
type FreshnessStatus string

const (
	FreshnessStatusFresh         FreshnessStatus = "fresh"
	FreshnessStatusDelayed       FreshnessStatus = "delayed"
	FreshnessStatusUnknown       FreshnessStatus = "unknown"
	FreshnessStatusNotApplicable FreshnessStatus = "not_applicable"
)

// USFreshnessInput is the database snapshot needed for one US regular-session
// subscription. LastClosedOpenTime is nil before the first successful bar.
type USFreshnessInput struct {
	ObservedAt         time.Time
	Interval           domain.BarInterval
	CloseDelay         time.Duration
	LastClosedOpenTime *time.Time
}

// USFreshnessResult is a pure status projection suitable for the later
// Provider status API. Delay counts only time inside regular trading sessions.
type USFreshnessResult struct {
	MarketState       MarketState
	FreshnessStatus   FreshnessStatus
	DataDelaySeconds  *int64
	ExpectedOpenTime  *time.Time
	ExpectedCloseTime *time.Time
	NextMarketOpenAt  *time.Time
}

// CalculateUSFreshness evaluates a US regular-session scope without accessing
// a Provider. Closed markets are always not_applicable and never accumulate
// delay, even if the last stored bar is old.
func CalculateUSFreshness(calendar markettime.TradingCalendar, input USFreshnessInput) (USFreshnessResult, error) {
	if calendar == nil || calendar.Location() == nil {
		return USFreshnessResult{}, errors.New("US freshness trading calendar is required")
	}
	if input.ObservedAt.IsZero() {
		return USFreshnessResult{}, invalidWindow("freshness observed time is required")
	}
	if input.CloseDelay < 0 {
		return USFreshnessResult{}, invalidWindow("freshness close delay cannot be negative")
	}
	if _, err := durationForInterval(input.Interval); err != nil {
		return USFreshnessResult{}, err
	}

	observedAt := input.ObservedAt.UTC()
	currentSession, open, err := calendar.SessionAt(observedAt)
	if err != nil {
		return USFreshnessResult{}, fmt.Errorf("resolve US market state: %w", err)
	}
	if !open {
		nextSession, err := calendar.NextSession(observedAt)
		if err != nil {
			return USFreshnessResult{}, fmt.Errorf("resolve next US market open: %w", err)
		}
		nextOpen := nextSession.Open.UTC()
		return USFreshnessResult{
			MarketState: MarketStateClosed, FreshnessStatus: FreshnessStatusNotApplicable,
			NextMarketOpenAt: &nextOpen,
		}, nil
	}

	expected, exists, err := latestEligibleUSBar(calendar, currentSession, input.Interval, observedAt, input.CloseDelay)
	if err != nil {
		return USFreshnessResult{}, fmt.Errorf("resolve expected US bar: %w", err)
	}
	if !exists {
		return USFreshnessResult{MarketState: MarketStateOpen, FreshnessStatus: FreshnessStatusUnknown}, nil
	}
	expectedOpen, expectedClose := expected.open, expected.close
	result := USFreshnessResult{
		MarketState: MarketStateOpen, FreshnessStatus: FreshnessStatusUnknown,
		ExpectedOpenTime: &expectedOpen, ExpectedCloseTime: &expectedClose,
	}
	if input.LastClosedOpenTime == nil {
		return result, nil
	}
	actual, err := usBarForOpen(calendar, input.Interval, *input.LastClosedOpenTime)
	if err != nil {
		return USFreshnessResult{}, fmt.Errorf("resolve last closed US bar: %w", err)
	}
	if actual.close.After(observedAt) {
		return USFreshnessResult{}, invalidWindow("last closed US bar has not closed at the observed time")
	}
	zero := int64(0)
	if !actual.open.Before(expected.open) {
		result.FreshnessStatus = FreshnessStatusFresh
		result.DataDelaySeconds = &zero
		return result, nil
	}
	delay, err := regularTradingDuration(calendar, actual.close, expected.close)
	if err != nil {
		return USFreshnessResult{}, fmt.Errorf("calculate US data delay: %w", err)
	}
	seconds := int64(delay / time.Second)
	result.FreshnessStatus = FreshnessStatusDelayed
	result.DataDelaySeconds = &seconds
	return result, nil
}

// CalculateLatestUSWindow returns the newest US regular-session bar whose
// close plus delay is due, including while the market is currently closed.
func CalculateLatestUSWindow(calendar markettime.TradingCalendar, interval domain.BarInterval, trigger WindowTrigger, observedAt time.Time, delay time.Duration) (*CollectionWindow, error) {
	if calendar == nil || calendar.Location() == nil || observedAt.IsZero() || delay < 0 ||
		(trigger != WindowTriggerClose && trigger != WindowTriggerRevision) {
		return nil, invalidWindow("latest US window input is invalid")
	}
	if _, err := durationForInterval(interval); err != nil {
		return nil, err
	}
	observedAt = observedAt.UTC()
	date, err := markettime.DateInLocation(observedAt, calendar.Location())
	if err != nil {
		return nil, err
	}
	session, exists, err := calendar.SessionForDate(date)
	if err != nil {
		return nil, err
	}
	if !exists || observedAt.Before(session.Open) {
		session, err = calendar.PreviousSession(observedAt)
		if errors.Is(err, markettime.ErrCalendarOutOfRange) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	bar, due, err := latestEligibleUSBar(calendar, session, interval, observedAt, delay)
	if err != nil || !due {
		return nil, err
	}
	return &CollectionWindow{
		Interval: interval, Trigger: trigger, RangeStart: bar.open.UTC(), RangeEnd: bar.close.UTC(),
		EligibleAt: bar.close.Add(delay).UTC(),
	}, nil
}

type scheduledUSBar struct {
	open  time.Time
	close time.Time
}

func latestEligibleUSBar(calendar markettime.TradingCalendar, session markettime.MarketSession, interval domain.BarInterval, observedAt time.Time, delay time.Duration) (scheduledUSBar, bool, error) {
	for {
		bars, err := barsForUSSession(session, interval)
		if err != nil {
			return scheduledUSBar{}, false, err
		}
		for index := len(bars) - 1; index >= 0; index-- {
			if !bars[index].close.Add(delay).After(observedAt) {
				return bars[index], true, nil
			}
		}
		previous, err := calendar.PreviousSession(session.Open)
		if errors.Is(err, markettime.ErrCalendarOutOfRange) {
			return scheduledUSBar{}, false, nil
		}
		if err != nil {
			return scheduledUSBar{}, false, err
		}
		session = previous
	}
}

func barsForUSSession(session markettime.MarketSession, interval domain.BarInterval) ([]scheduledUSBar, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	switch interval {
	case domain.BarInterval1Day:
		open := time.Date(session.Date.Year, session.Date.Month, session.Date.Day, 0, 0, 0, 0, time.UTC)
		return []scheduledUSBar{{open: open, close: session.Close.UTC()}}, nil
	case domain.BarInterval1Hour:
		bars := make([]scheduledUSBar, 0, 7)
		for open := session.Open.UTC(); open.Before(session.Close); {
			closeAt := open.Add(time.Hour)
			if closeAt.After(session.Close) {
				closeAt = session.Close.UTC()
			}
			bars = append(bars, scheduledUSBar{open: open, close: closeAt})
			open = closeAt
		}
		return bars, nil
	default:
		return nil, invalidWindow("unsupported US bar interval")
	}
}

func usBarForOpen(calendar markettime.TradingCalendar, interval domain.BarInterval, open time.Time) (scheduledUSBar, error) {
	if open.IsZero() {
		return scheduledUSBar{}, invalidWindow("US bar open time is required")
	}
	open = open.UTC()
	var date markettime.MarketDate
	var err error
	if interval == domain.BarInterval1Day {
		date, err = markettime.UTCDate(open)
	} else {
		date, err = markettime.DateInLocation(open, calendar.Location())
	}
	if err != nil {
		return scheduledUSBar{}, err
	}
	session, exists, err := calendar.SessionForDate(date)
	if err != nil {
		return scheduledUSBar{}, err
	}
	if !exists {
		return scheduledUSBar{}, invalidWindow("US bar open time belongs to a closed market date")
	}
	bars, err := barsForUSSession(session, interval)
	if err != nil {
		return scheduledUSBar{}, err
	}
	for _, bar := range bars {
		if bar.open.Equal(open) {
			return bar, nil
		}
	}
	return scheduledUSBar{}, invalidWindow("US bar open time is not aligned to a regular-session bar")
}

func regularTradingDuration(calendar markettime.TradingCalendar, from, to time.Time) (time.Duration, error) {
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		return 0, nil
	}
	var total time.Duration
	cursor := from
	for cursor.Before(to) {
		session, active, err := calendar.SessionAt(cursor)
		if err != nil {
			return 0, err
		}
		if !active {
			session, err = calendar.NextSession(cursor)
			if err != nil {
				return 0, err
			}
		}
		start := cursor
		if start.Before(session.Open) {
			start = session.Open
		}
		end := to
		if end.After(session.Close) {
			end = session.Close
		}
		if start.Before(end) {
			total += end.Sub(start)
		}
		if !session.Close.Before(to) {
			break
		}
		cursor = session.Close
	}
	return total, nil
}
