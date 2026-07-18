package markettime

import (
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

func TestNYSECalendarSessionsCoverDSTHolidaysAndEarlyCloses(t *testing.T) {
	calendar := mustCalendar(t)
	tests := []struct {
		name      string
		date      MarketDate
		wantOpen  string
		wantClose string
		wantNone  bool
	}{
		{"winter EST", MarketDate{2026, time.January, 2}, "2026-01-02T14:30:00Z", "2026-01-02T21:00:00Z", false},
		{"summer EDT", MarketDate{2026, time.July, 2}, "2026-07-02T13:30:00Z", "2026-07-02T20:00:00Z", false},
		{"observed independence holiday", MarketDate{2026, time.July, 3}, "", "", true},
		{"weekend", MarketDate{2026, time.July, 4}, "", "", true},
		{"thanksgiving", MarketDate{2026, time.November, 26}, "", "", true},
		{"day after thanksgiving early close", MarketDate{2026, time.November, 27}, "2026-11-27T14:30:00Z", "2026-11-27T18:00:00Z", false},
		{"christmas eve early close", MarketDate{2026, time.December, 24}, "2026-12-24T14:30:00Z", "2026-12-24T18:00:00Z", false},
		{"2028 new year Saturday has no Friday observation", MarketDate{2027, time.December, 31}, "2027-12-31T14:30:00Z", "2027-12-31T21:00:00Z", false},
		{"2028 July early close", MarketDate{2028, time.July, 3}, "2028-07-03T13:30:00Z", "2028-07-03T17:00:00Z", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, exists, err := calendar.SessionForDate(test.date)
			if err != nil {
				t.Fatalf("SessionForDate() error = %v", err)
			}
			if test.wantNone {
				if exists {
					t.Fatalf("SessionForDate() = %#v, want closed", session)
				}
				return
			}
			if !exists || session.Date != test.date || session.Open != mustTime(t, test.wantOpen) || session.Close != mustTime(t, test.wantClose) {
				t.Fatalf("SessionForDate() = (%#v, %t)", session, exists)
			}
		})
	}
}

func TestNYSECalendarSessionNavigationAndBoundaries(t *testing.T) {
	calendar := mustCalendar(t)
	open := mustTime(t, "2026-07-06T13:30:00Z")
	closeAt := mustTime(t, "2026-07-06T20:00:00Z")
	if _, active, err := calendar.SessionAt(open); err != nil || !active {
		t.Fatalf("SessionAt(open) = (%t, %v)", active, err)
	}
	if _, active, err := calendar.SessionAt(closeAt); err != nil || active {
		t.Fatalf("SessionAt(close) = (%t, %v)", active, err)
	}
	if _, active, err := calendar.SessionAt(mustTime(t, "2026-07-03T15:00:00Z")); err != nil || active {
		t.Fatalf("SessionAt(holiday) = (%t, %v)", active, err)
	}

	next, err := calendar.NextSession(mustTime(t, "2026-07-02T20:00:00Z"))
	if err != nil || next.Open != open {
		t.Fatalf("NextSession() = (%#v, %v)", next, err)
	}
	previous, err := calendar.PreviousSession(open)
	if err != nil || previous.Date != (MarketDate{2026, time.July, 2}) {
		t.Fatalf("PreviousSession() = (%#v, %v)", previous, err)
	}
	if calendar.Location() == nil || calendar.Location().String() != "America/New_York" {
		t.Fatalf("Location() = %v", calendar.Location())
	}
}

func TestNYSECalendarOverridesAndValidation(t *testing.T) {
	closure := SessionOverride{Date: MarketDate{2026, time.July, 6}, Closed: true}
	early := SessionOverride{Date: MarketDate{2026, time.July, 7}, CloseHour: 14}
	calendar, err := NewNYSECalendar(closure, early)
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	if _, exists, err := calendar.SessionForDate(closure.Date); err != nil || exists {
		t.Fatalf("SessionForDate(closure) = (%t, %v)", exists, err)
	}
	session, exists, err := calendar.SessionForDate(early.Date)
	if err != nil || !exists || session.Close != mustTime(t, "2026-07-07T18:00:00Z") {
		t.Fatalf("SessionForDate(early) = (%#v, %t, %v)", session, exists, err)
	}

	tests := []SessionOverride{
		{Date: MarketDate{2025, time.July, 1}, Closed: true},
		{Date: MarketDate{2026, time.February, 30}, Closed: true},
		{Date: MarketDate{2026, time.July, 8}, Closed: true, CloseHour: 13},
		{Date: MarketDate{2026, time.July, 8}, CloseHour: 9, CloseMinute: 30},
		{Date: MarketDate{2026, time.July, 8}, CloseHour: 16, CloseMinute: 1},
	}
	for _, value := range tests {
		if _, err := NewNYSECalendar(value); err == nil {
			t.Fatalf("NewNYSECalendar(%#v) error = nil", value)
		}
	}
}

func TestCalendarRejectsInvalidAndUnsupportedInputs(t *testing.T) {
	calendar := mustCalendar(t)
	if _, err := NewMarketDate(2026, time.February, 30); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("NewMarketDate() error = %v", err)
	}
	if _, err := DateInLocation(time.Time{}, time.UTC); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("DateInLocation(zero) error = %v", err)
	}
	if _, err := UTCDate(time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("UTCDate(zero) error = %v", err)
	}
	if _, _, err := calendar.SessionForDate(MarketDate{2029, time.January, 2}); !errors.Is(err, ErrCalendarOutOfRange) {
		t.Fatalf("SessionForDate(2029) error = %v", err)
	}
	if _, _, err := calendar.SessionAt(time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("SessionAt(zero) error = %v", err)
	}
	if _, err := calendar.NextSession(time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("NextSession(zero) error = %v", err)
	}
	if _, err := calendar.PreviousSession(time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("PreviousSession(zero) error = %v", err)
	}
	var nilCalendar *NYSECalendar
	if nilCalendar.Location() != nil {
		t.Fatal("nil calendar location is not nil")
	}
	if _, _, err := nilCalendar.SessionForDate(MarketDate{2026, time.July, 1}); err == nil {
		t.Fatal("nil calendar SessionForDate() error = nil")
	}
}

func mustCalendar(t *testing.T) *NYSECalendar {
	t.Helper()
	calendar, err := NewNYSECalendar()
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	return calendar
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", value, err)
	}
	return parsed.UTC()
}
