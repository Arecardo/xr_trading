// Package markettime owns exchange-session calendars shared by schedulers,
// freshness calculations, and provider normalization.
package markettime

import (
	"errors"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

const (
	firstNYSEYear = 2026
	lastNYSEYear  = 2028
)

// ErrCalendarOutOfRange prevents callers from treating an unverified year as
// an ordinary trading year.
var ErrCalendarOutOfRange = errors.New("trading calendar is outside its supported range")

// MarketDate is a civil date in an exchange calendar, without an ambiguous
// timezone conversion.
type MarketDate struct {
	Year  int
	Month time.Month
	Day   int
}

// NewMarketDate validates a civil date.
func NewMarketDate(year int, month time.Month, day int) (MarketDate, error) {
	date := MarketDate{Year: year, Month: month, Day: day}
	if err := date.Validate(); err != nil {
		return MarketDate{}, err
	}
	return date, nil
}

// DateInLocation extracts the civil date at an exchange location.
func DateInLocation(value time.Time, location *time.Location) (MarketDate, error) {
	if value.IsZero() || location == nil {
		return MarketDate{}, invalidCalendar("time and location are required")
	}
	year, month, day := value.In(location).Date()
	return NewMarketDate(year, month, day)
}

// UTCDate extracts the UTC date used by Longbridge daily-bar timestamps.
func UTCDate(value time.Time) (MarketDate, error) {
	if value.IsZero() {
		return MarketDate{}, invalidCalendar("time is required")
	}
	year, month, day := value.UTC().Date()
	return NewMarketDate(year, month, day)
}

// Validate rejects impossible civil dates.
func (date MarketDate) Validate() error {
	if date.Year < 1 || date.Month < time.January || date.Month > time.December || date.Day < 1 || date.Day > 31 {
		return invalidCalendar("market date is invalid")
	}
	value := time.Date(date.Year, date.Month, date.Day, 0, 0, 0, 0, time.UTC)
	if value.Year() != date.Year || value.Month() != date.Month || value.Day() != date.Day {
		return invalidCalendar("market date is invalid")
	}
	return nil
}

func (date MarketDate) String() string {
	if date.Validate() != nil {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", date.Year, date.Month, date.Day)
}

func (date MarketDate) addDays(days int) MarketDate {
	value := time.Date(date.Year, date.Month, date.Day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	return MarketDate{Year: value.Year(), Month: value.Month(), Day: value.Day()}
}

// MarketSession is one regular equity session. Open is inclusive and Close is
// exclusive for market-state checks.
type MarketSession struct {
	Date  MarketDate
	Open  time.Time
	Close time.Time
}

// Validate checks the session identity and UTC-normalized bounds.
func (session MarketSession) Validate() error {
	if err := session.Date.Validate(); err != nil {
		return err
	}
	if session.Open.IsZero() || session.Close.IsZero() || !session.Open.Before(session.Close) {
		return invalidCalendar("market session bounds are invalid")
	}
	return nil
}

// TradingCalendar is the read-only market-time port. Implementations must not
// infer sessions outside an explicitly supported calendar range.
type TradingCalendar interface {
	Location() *time.Location
	SessionForDate(MarketDate) (MarketSession, bool, error)
	SessionAt(time.Time) (MarketSession, bool, error)
	NextSession(time.Time) (MarketSession, error)
	PreviousSession(time.Time) (MarketSession, error)
}

// SessionOverride represents an exceptional full closure or early close.
// CloseHour/CloseMinute are ignored for a full closure.
type SessionOverride struct {
	Date        MarketDate
	Closed      bool
	CloseHour   int
	CloseMinute int
}

// NYSECalendar is the verified first-phase NYSE regular-session calendar for
// 2026-2028. Its timezone database performs EST/EDT conversion.
type NYSECalendar struct {
	location *time.Location
	special  map[MarketDate]sessionRule
}

type sessionRule struct {
	closed      bool
	closeHour   int
	closeMinute int
}

// NewNYSECalendar constructs the official 2026-2028 calendar and applies
// optional exceptional closures or early closes.
func NewNYSECalendar(overrides ...SessionOverride) (*NYSECalendar, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("load NYSE timezone: %w", err)
	}
	calendar := &NYSECalendar{location: location, special: officialNYSERules()}
	for _, override := range overrides {
		if err := calendar.applyOverride(override); err != nil {
			return nil, err
		}
	}
	return calendar, nil
}

// Location returns the immutable exchange timezone.
func (calendar *NYSECalendar) Location() *time.Location {
	if calendar == nil {
		return nil
	}
	return calendar.location
}

// SessionForDate returns the regular session or exists=false for weekends and
// verified exchange holidays.
func (calendar *NYSECalendar) SessionForDate(date MarketDate) (MarketSession, bool, error) {
	if calendar == nil || calendar.location == nil {
		return MarketSession{}, false, errors.New("NYSE calendar is not initialized")
	}
	if err := date.Validate(); err != nil {
		return MarketSession{}, false, err
	}
	if date.Year < firstNYSEYear || date.Year > lastNYSEYear {
		return MarketSession{}, false, fmt.Errorf("NYSE date %s: %w", date, ErrCalendarOutOfRange)
	}
	localNoon := time.Date(date.Year, date.Month, date.Day, 12, 0, 0, 0, calendar.location)
	if localNoon.Weekday() == time.Saturday || localNoon.Weekday() == time.Sunday {
		return MarketSession{}, false, nil
	}
	rule, special := calendar.special[date]
	if special && rule.closed {
		return MarketSession{}, false, nil
	}
	closeHour, closeMinute := 16, 0
	if special {
		closeHour, closeMinute = rule.closeHour, rule.closeMinute
	}
	session := MarketSession{
		Date:  date,
		Open:  time.Date(date.Year, date.Month, date.Day, 9, 30, 0, 0, calendar.location).UTC(),
		Close: time.Date(date.Year, date.Month, date.Day, closeHour, closeMinute, 0, 0, calendar.location).UTC(),
	}
	if err := session.Validate(); err != nil {
		return MarketSession{}, false, err
	}
	return session, true, nil
}

// SessionAt reports whether an instant lies in the core session.
func (calendar *NYSECalendar) SessionAt(value time.Time) (MarketSession, bool, error) {
	if value.IsZero() {
		return MarketSession{}, false, invalidCalendar("session instant is required")
	}
	date, err := DateInLocation(value, calendar.Location())
	if err != nil {
		return MarketSession{}, false, err
	}
	session, exists, err := calendar.SessionForDate(date)
	if err != nil || !exists {
		return MarketSession{}, false, err
	}
	value = value.UTC()
	return session, !value.Before(session.Open) && value.Before(session.Close), nil
}

// NextSession returns the first session whose open is strictly after value.
func (calendar *NYSECalendar) NextSession(value time.Time) (MarketSession, error) {
	if value.IsZero() {
		return MarketSession{}, invalidCalendar("next-session instant is required")
	}
	date, err := DateInLocation(value, calendar.Location())
	if err != nil {
		return MarketSession{}, err
	}
	for {
		session, exists, err := calendar.SessionForDate(date)
		if err != nil {
			return MarketSession{}, err
		}
		if exists && session.Open.After(value.UTC()) {
			return session, nil
		}
		date = date.addDays(1)
	}
}

// PreviousSession returns the latest session whose close is at or before value.
func (calendar *NYSECalendar) PreviousSession(value time.Time) (MarketSession, error) {
	if value.IsZero() {
		return MarketSession{}, invalidCalendar("previous-session instant is required")
	}
	date, err := DateInLocation(value, calendar.Location())
	if err != nil {
		return MarketSession{}, err
	}
	for {
		session, exists, err := calendar.SessionForDate(date)
		if err != nil {
			return MarketSession{}, err
		}
		if exists && !session.Close.After(value.UTC()) {
			return session, nil
		}
		date = date.addDays(-1)
	}
}

func (calendar *NYSECalendar) applyOverride(override SessionOverride) error {
	if err := override.Date.Validate(); err != nil {
		return fmt.Errorf("apply NYSE override: %w", err)
	}
	if override.Date.Year < firstNYSEYear || override.Date.Year > lastNYSEYear {
		return fmt.Errorf("apply NYSE override for %s: %w", override.Date, ErrCalendarOutOfRange)
	}
	if override.Closed {
		if override.CloseHour != 0 || override.CloseMinute != 0 {
			return invalidCalendar("closed NYSE override cannot also define a close time")
		}
		calendar.special[override.Date] = sessionRule{closed: true}
		return nil
	}
	if override.CloseHour < 9 || override.CloseHour > 16 || override.CloseMinute < 0 || override.CloseMinute > 59 ||
		(override.CloseHour == 9 && override.CloseMinute <= 30) || (override.CloseHour == 16 && override.CloseMinute != 0) {
		return invalidCalendar("NYSE override close must be after 09:30 and no later than 16:00")
	}
	calendar.special[override.Date] = sessionRule{closeHour: override.CloseHour, closeMinute: override.CloseMinute}
	return nil
}

func officialNYSERules() map[MarketDate]sessionRule {
	rules := make(map[MarketDate]sessionRule)
	closed := []MarketDate{
		{2026, time.January, 1}, {2026, time.January, 19}, {2026, time.February, 16}, {2026, time.April, 3}, {2026, time.May, 25}, {2026, time.June, 19}, {2026, time.July, 3}, {2026, time.September, 7}, {2026, time.November, 26}, {2026, time.December, 25},
		{2027, time.January, 1}, {2027, time.January, 18}, {2027, time.February, 15}, {2027, time.March, 26}, {2027, time.May, 31}, {2027, time.June, 18}, {2027, time.July, 5}, {2027, time.September, 6}, {2027, time.November, 25}, {2027, time.December, 24},
		{2028, time.January, 17}, {2028, time.February, 21}, {2028, time.April, 14}, {2028, time.May, 29}, {2028, time.June, 19}, {2028, time.July, 4}, {2028, time.September, 4}, {2028, time.November, 23}, {2028, time.December, 25},
	}
	for _, date := range closed {
		rules[date] = sessionRule{closed: true}
	}
	for _, date := range []MarketDate{
		{2026, time.November, 27}, {2026, time.December, 24},
		{2027, time.November, 26},
		{2028, time.July, 3}, {2028, time.November, 24},
	} {
		rules[date] = sessionRule{closeHour: 13}
	}
	return rules
}

func invalidCalendar(message string) error {
	return fmt.Errorf("%s: %w", message, domain.ErrInvalidData)
}

var _ TradingCalendar = (*NYSECalendar)(nil)
