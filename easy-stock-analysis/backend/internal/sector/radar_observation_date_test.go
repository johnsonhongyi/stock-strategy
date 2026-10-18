package sector

import (
	"testing"
	"time"
)

func TestIndustryObservationDateUsesTradingCalendar(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, sample := range []struct {
		day  string
		want string
	}{{"2026-10-18 12:00", "2026-10-16"}, {"2026-10-19 08:00", "2026-10-16"}, {"2026-10-19 10:00", "2026-10-19"}} {
		now, err := time.ParseInLocation("2006-01-02 15:04", sample.day, location)
		if err != nil {
			t.Fatal(err)
		}
		if got := industryObservationDate(now); got != sample.want {
			t.Fatalf("%s: got %s want %s", sample.day, got, sample.want)
		}
	}
}
