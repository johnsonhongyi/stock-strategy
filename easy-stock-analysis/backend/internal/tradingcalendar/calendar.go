package tradingcalendar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type dateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type marketOverrides struct {
	ClosedRanges       []dateRange `json:"closed_ranges"`
	ManualClosedDates  []string    `json:"manual_closed_dates"`
	ManualOpenDates    []string    `json:"manual_open_dates"`
}

type calendarFile struct {
	SchemaVersion int                        `json:"schema_version"`
	Markets       map[string]marketOverrides `json:"markets"`
}

type fileSnapshot struct {
	path    string
	modTime int64
	size    int64
	data    calendarFile
}

var calendarCache struct {
	sync.Mutex
	snapshot fileSnapshot
	loaded   bool
}

var defaultCalendar = calendarFile{
	SchemaVersion: 1,
	Markets: map[string]marketOverrides{
		"CN": {ClosedRanges: []dateRange{
			{Start: "2026-01-01", End: "2026-01-03"},
			{Start: "2026-02-15", End: "2026-02-23"},
			{Start: "2026-04-04", End: "2026-04-06"},
			{Start: "2026-05-01", End: "2026-05-05"},
			{Start: "2026-06-19", End: "2026-06-21"},
			{Start: "2026-09-25", End: "2026-09-27"},
			{Start: "2026-10-01", End: "2026-10-07"},
		}},
		"US": {},
	},
}

func calendarPath() string {
	if path := strings.TrimSpace(os.Getenv("A_STOCK_TRADING_CALENDAR")); path != "" {
		return path
	}
	if root := strings.TrimSpace(os.Getenv("A_STOCK_DATA_ROOT")); root != "" {
		return filepath.Join(root, "config", "trading-calendar.json")
	}
	for _, candidate := range []string{
		"trading-calendar.json",
		filepath.Join("..", "..", "easy-stock-service", "trading-calendar.json"),
		filepath.Join("source", "easy-stock-service", "trading-calendar.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "trading-calendar.json"
}

func loadCalendar() (calendarFile, error) {
	path := calendarPath()
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultCalendar, nil
		}
		return calendarFile{}, fmt.Errorf("stat trading calendar %s: %w", path, err)
	}
	modTime := stat.ModTime().UnixNano()
	calendarCache.Lock()
	defer calendarCache.Unlock()
	if calendarCache.loaded && calendarCache.snapshot.path == path &&
		calendarCache.snapshot.modTime == modTime && calendarCache.snapshot.size == stat.Size() {
		return calendarCache.snapshot.data, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return calendarFile{}, fmt.Errorf("open trading calendar %s: %w", path, err)
	}
	defer file.Close()
	var data calendarFile
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return calendarFile{}, fmt.Errorf("decode trading calendar %s: %w", path, err)
	}
	if data.SchemaVersion != 1 || data.Markets == nil {
		return calendarFile{}, fmt.Errorf("unsupported trading calendar schema in %s", path)
	}
	calendarCache.snapshot = fileSnapshot{path: path, modTime: modTime, size: stat.Size(), data: data}
	calendarCache.loaded = true
	return data, nil
}

// LatestCompletedDate returns the last fully completed exchange session in the market's timezone.
func LatestCompletedDate(market string, now time.Time) (string, error) {
	market = strings.ToUpper(strings.TrimSpace(market))
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeHour, closeMinute := 15, 0
	switch market {
	case "US":
		var err error
		location, err = time.LoadLocation("America/New_York")
		if err != nil {
			return "", fmt.Errorf("load US exchange timezone: %w", err)
		}
		closeHour, closeMinute = 16, 0
	case "CRYPTO":
		location = time.UTC
	default:
		market = "CN"
	}
	localNow := now.In(location)
	year, month, day := localNow.Date()
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if market == "CRYPTO" {
		return date.AddDate(0, 0, -1).Format("2006-01-02"), nil
	}
	calendar, err := loadCalendar()
	if err != nil {
		return "", err
	}
	if localNow.Hour() < closeHour ||
		(localNow.Hour() == closeHour && localNow.Minute() < closeMinute) {
		date = date.AddDate(0, 0, -1)
	}
	for i := 0; i < 370; i++ {
		dateString := date.Format("2006-01-02")
		open, err := isTradingDate(date, market, calendar)
		if err != nil {
			return "", err
		}
		if open {
			return dateString, nil
		}
		date = date.AddDate(0, 0, -1)
	}
	return "", fmt.Errorf("no completed trading session found for %s", market)
}

// IsTradingDate checks the shared manual calendar plus recurring US exchange holidays.
func IsTradingDate(dateString string, market string) (bool, error) {
	date, err := time.Parse("2006-01-02", strings.TrimSpace(dateString))
	if err != nil {
		return false, fmt.Errorf("invalid trading date %q: %w", dateString, err)
	}
	market = strings.ToUpper(strings.TrimSpace(market))
	if market == "CRYPTO" {
		return true, nil
	}
	if market != "US" {
		market = "CN"
	}
	calendar, err := loadCalendar()
	if err != nil {
		return false, err
	}
	return isTradingDate(date, market, calendar)
}

func isTradingDate(date time.Time, market string, calendar calendarFile) (bool, error) {
	dateString := date.Format("2006-01-02")
	overrides := calendar.Markets[market]
	if containsDate(overrides.ManualOpenDates, dateString) {
		return true, nil
	}
	if containsDate(overrides.ManualClosedDates, dateString) {
		return false, nil
	}
	if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
		return false, nil
	}
	if market == "US" {
		return !usExchangeHolidays(date.Year())[dateString], nil
	}
	for _, interval := range overrides.ClosedRanges {
		if _, err := time.Parse("2006-01-02", interval.Start); err != nil {
			return false, fmt.Errorf("invalid CN closed range start %q: %w", interval.Start, err)
		}
		if _, err := time.Parse("2006-01-02", interval.End); err != nil {
			return false, fmt.Errorf("invalid CN closed range end %q: %w", interval.End, err)
		}
		if interval.Start <= dateString && dateString <= interval.End {
			return false, nil
		}
	}
	return true, nil
}

func containsDate(dates []string, target string) bool {
	for _, date := range dates {
		if strings.TrimSpace(date) == target {
			return true
		}
	}
	return false
}

func usExchangeHolidays(year int) map[string]bool {
	holidays := make(map[string]bool)
	for holidayYear := year - 1; holidayYear <= year+1; holidayYear++ {
		newYear := time.Date(holidayYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		// NYSE does not observe New Year's Day when January 1 is Saturday.
		if newYear.Weekday() == time.Sunday {
			addHoliday(holidays, newYear.AddDate(0, 0, 1))
		} else if newYear.Weekday() >= time.Monday && newYear.Weekday() <= time.Friday {
			addHoliday(holidays, newYear)
		}
		if holidayYear >= 1998 {
			addHoliday(holidays, weekdayInMonth(holidayYear, time.January, time.Monday, 3))
		}
		addHoliday(holidays, weekdayInMonth(holidayYear, time.February, time.Monday, 3))
		addHoliday(holidays, easterSunday(holidayYear).AddDate(0, 0, -2))
		addHoliday(holidays, lastWeekdayInMonth(holidayYear, time.May, time.Monday))
		if holidayYear >= 2022 {
			addHoliday(holidays, observedFixedDate(time.Date(holidayYear, time.June, 19, 0, 0, 0, 0, time.UTC)))
		}
		addHoliday(holidays, observedFixedDate(time.Date(holidayYear, time.July, 4, 0, 0, 0, 0, time.UTC)))
		addHoliday(holidays, weekdayInMonth(holidayYear, time.September, time.Monday, 1))
		addHoliday(holidays, weekdayInMonth(holidayYear, time.November, time.Thursday, 4))
		addHoliday(holidays, observedFixedDate(time.Date(holidayYear, time.December, 25, 0, 0, 0, 0, time.UTC)))
	}
	return holidays
}

func addHoliday(holidays map[string]bool, day time.Time) {
	holiday := day.Format("2006-01-02")
	holidays[holiday] = true
}

func observedFixedDate(day time.Time) time.Time {
	switch day.Weekday() {
	case time.Saturday:
		return day.AddDate(0, 0, -1)
	case time.Sunday:
		return day.AddDate(0, 0, 1)
	default:
		return day
	}
}

func weekdayInMonth(year int, month time.Month, weekday time.Weekday, occurrence int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(weekday) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+7*(occurrence-1))
}

func lastWeekdayInMonth(year int, month time.Month, weekday time.Weekday) time.Time {
	firstNext := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	last := firstNext.AddDate(0, 0, -1)
	offset := (int(last.Weekday()) - int(weekday) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

func easterSunday(year int) time.Time {
	a := year % 19
	b, c := year/100, year%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	ell := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*ell) / 451
	month := (h + ell - 7*m + 114) / 31
	day := (h+ell-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
