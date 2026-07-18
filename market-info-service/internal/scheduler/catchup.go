package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
)

// catchUpWindows uses the checkpoint only as a continuation hint. The current
// closed market_bars remain the completeness fact and decide which ranges are
// actually missing.
func (scheduler *IncrementalScheduler) catchUpWindows(ctx context.Context, target SchedulingTarget, delay time.Duration, observedAt time.Time) ([]CollectionWindow, error) {
	latest, err := scheduler.latestWindow(target, WindowTriggerClose, delay, observedAt)
	if err != nil || latest == nil || !latest.RangeEnd.After(target.Subscription.UpdatedAt.UTC()) {
		return nil, err
	}
	checkpoint, err := scheduler.store.LoadSchedulingCheckpoint(ctx, target.Subscription.ID)
	if err != nil {
		return nil, fmt.Errorf("load scheduling checkpoint: %w", err)
	}
	boundary := target.Subscription.UpdatedAt.UTC()
	if checkpoint != nil {
		if err := validateCheckpoint(target.Subscription.ID, *checkpoint); err != nil {
			return nil, err
		}
		if checkpoint.LastClosedOpenTime != nil {
			bar, err := scheduler.barForOpen(target, *checkpoint.LastClosedOpenTime)
			if err != nil {
				return nil, fmt.Errorf("resolve checkpoint bar: %w", err)
			}
			prefix, err := scheduler.expectedCloseWindowsUntil(target, target.Subscription.UpdatedAt.UTC(), CollectionWindow{
				Interval: latest.Interval, Trigger: WindowTriggerClose, RangeStart: bar.open, RangeEnd: bar.close, EligibleAt: bar.close.Add(delay),
			}, delay)
			if err != nil {
				return nil, fmt.Errorf("plan checkpoint prefix: %w", err)
			}
			var existing []time.Time
			if len(prefix) > 0 {
				existing, err = scheduler.store.ListClosedBarOpenTimes(ctx, target, prefix[0].RangeStart, prefix[len(prefix)-1].RangeEnd)
				if err != nil {
					return nil, fmt.Errorf("verify scheduling checkpoint: %w", err)
				}
			}
			if len(prefix) > 0 && allWindowsPresent(prefix, existing) {
				boundary = bar.close
			}
		}
	}

	candidates, err := scheduler.expectedCloseWindows(target, boundary, *latest, delay)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	existing, err := scheduler.store.ListClosedBarOpenTimes(ctx, target, candidates[0].RangeStart, candidates[len(candidates)-1].RangeEnd)
	if err != nil {
		return nil, fmt.Errorf("list closed bars for catch-up: %w", err)
	}
	return missingWindowGroups(candidates, existing, scheduler.maximumCatchUpBars, scheduler.maximumCatchUpRuns), nil
}

func validateCheckpoint(subscriptionID domain.ID, checkpoint domain.IngestionCheckpoint) error {
	if checkpoint.SubscriptionID != subscriptionID || checkpoint.UpdatedAt.IsZero() || checkpoint.ConsecutiveFailures < 0 ||
		(checkpoint.LastClosedOpenTime != nil && checkpoint.LastClosedOpenTime.IsZero()) {
		return fmt.Errorf("invalid scheduling checkpoint: %w", domain.ErrInvalidData)
	}
	return nil
}

func (scheduler *IncrementalScheduler) expectedCloseWindows(target SchedulingTarget, boundary time.Time, latest CollectionWindow, delay time.Duration) ([]CollectionWindow, error) {
	return scheduler.expectedCloseWindowsUntil(target, boundary, latest, delay)
}

func (scheduler *IncrementalScheduler) expectedCloseWindowsUntil(target SchedulingTarget, boundary time.Time, latest CollectionWindow, delay time.Duration) ([]CollectionWindow, error) {
	interval, err := domain.ParseBarInterval(target.Subscription.Interval)
	if err != nil {
		return nil, err
	}
	market, err := schedulingMarket(target)
	if err != nil {
		return nil, err
	}
	if market == "continuous" {
		return expectedContinuousWindows(interval, boundary, latest, delay, 0)
	}
	return expectedUSWindows(scheduler.usCalendar, interval, boundary, latest, delay, 0)
}

func expectedContinuousWindows(interval domain.BarInterval, boundary time.Time, latest CollectionWindow, delay time.Duration, limit int) ([]CollectionWindow, error) {
	duration, err := durationForInterval(interval)
	if err != nil {
		return nil, err
	}
	open := boundary.UTC().Truncate(duration)
	if !open.Add(duration).After(boundary.UTC()) {
		open = open.Add(duration)
	}
	windows := make([]CollectionWindow, 0)
	for (limit <= 0 || len(windows) < limit) && !open.After(latest.RangeStart) {
		closeAt := open.Add(duration)
		windows = append(windows, CollectionWindow{
			Interval: interval, Trigger: WindowTriggerClose, RangeStart: open, RangeEnd: closeAt, EligibleAt: closeAt.Add(delay),
		})
		open = closeAt
	}
	return windows, nil
}

func expectedUSWindows(calendar markettime.TradingCalendar, interval domain.BarInterval, boundary time.Time, latest CollectionWindow, delay time.Duration, limit int) ([]CollectionWindow, error) {
	if calendar == nil || calendar.Location() == nil {
		return nil, errors.New("US catch-up trading calendar is required")
	}
	date, err := markettime.DateInLocation(boundary, calendar.Location())
	if err != nil {
		return nil, err
	}
	windows := make([]CollectionWindow, 0)
	for limit <= 0 || len(windows) < limit {
		session, exists, err := calendar.SessionForDate(date)
		if err != nil {
			return nil, err
		}
		if exists {
			bars, err := barsForUSSession(session, interval)
			if err != nil {
				return nil, err
			}
			for _, bar := range bars {
				if bar.close.After(boundary.UTC()) && !bar.open.After(latest.RangeStart) {
					windows = append(windows, CollectionWindow{
						Interval: interval, Trigger: WindowTriggerClose, RangeStart: bar.open, RangeEnd: bar.close, EligibleAt: bar.close.Add(delay),
					})
					if limit > 0 && len(windows) == limit {
						return windows, nil
					}
				}
			}
		}
		if !sessionDateBefore(date, latest.RangeEnd, calendar.Location()) {
			break
		}
		value := time.Date(date.Year, date.Month, date.Day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		date, err = markettime.NewMarketDate(value.Year(), value.Month(), value.Day())
		if err != nil {
			return nil, err
		}
	}
	return windows, nil
}

func sessionDateBefore(date markettime.MarketDate, end time.Time, location *time.Location) bool {
	year, month, day := end.In(location).Date()
	return date.Year < year || (date.Year == year && (date.Month < month || (date.Month == month && date.Day < day)))
}

func (scheduler *IncrementalScheduler) barForOpen(target SchedulingTarget, open time.Time) (scheduledUSBar, error) {
	interval, err := domain.ParseBarInterval(target.Subscription.Interval)
	if err != nil {
		return scheduledUSBar{}, err
	}
	market, err := schedulingMarket(target)
	if err != nil {
		return scheduledUSBar{}, err
	}
	if market == "us_regular" {
		return usBarForOpen(scheduler.usCalendar, interval, open)
	}
	duration, err := durationForInterval(interval)
	if err != nil {
		return scheduledUSBar{}, err
	}
	open = open.UTC()
	if !open.Equal(open.Truncate(duration)) {
		return scheduledUSBar{}, invalidWindow("checkpoint open time is not interval aligned")
	}
	return scheduledUSBar{open: open, close: open.Add(duration)}, nil
}

func missingWindowGroups(candidates []CollectionWindow, existing []time.Time, maximumBars, maximumGroups int) []CollectionWindow {
	present := make(map[int64]struct{}, len(existing))
	for _, open := range existing {
		present[open.UTC().UnixNano()] = struct{}{}
	}
	groups := make([]CollectionWindow, 0, maximumGroups)
	for index := 0; index < len(candidates) && len(groups) < maximumGroups; {
		if _, ok := present[candidates[index].RangeStart.UnixNano()]; ok {
			index++
			continue
		}
		group := candidates[index]
		index++
		barCount := 1
		for index < len(candidates) && barCount < maximumBars {
			if _, ok := present[candidates[index].RangeStart.UnixNano()]; ok {
				break
			}
			group.RangeEnd = candidates[index].RangeEnd
			group.EligibleAt = candidates[index].EligibleAt
			index++
			barCount++
		}
		groups = append(groups, group)
	}
	return groups
}

func allWindowsPresent(candidates []CollectionWindow, existing []time.Time) bool {
	present := make(map[int64]struct{}, len(existing))
	for _, open := range existing {
		present[open.UTC().UnixNano()] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := present[candidate.RangeStart.UnixNano()]; !ok {
			return false
		}
	}
	return true
}
