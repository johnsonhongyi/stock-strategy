package sbc

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"easy-stock/backend/internal/foundation"
)

func TestATSParity(t *testing.T) {
	file, err := os.Open("testdata/ats_v1.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	var cases []struct {
		Name     string
		Input    json.RawMessage
		Expected any
	}
	if err := json.NewDecoder(data).Decode(&cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			output, err := Analyze(context.Background(), tc.Input)
			if err != nil {
				t.Fatal(err)
			}
			var actual any
			if err := json.Unmarshal(output, &actual); err != nil {
				t.Fatal(err)
			}
			compareATS(t, "", tc.Expected, actual)
		})
	}
}
func compareATS(t *testing.T, path string, expected, actual any) {
	t.Helper()
	switch want := expected.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			t.Fatalf("%s type mismatch", path)
		}
		for key, value := range want {
			next, exists := got[key]
			if !exists {
				t.Fatalf("%s.%s missing", path, key)
			}
			compareATS(t, path+"."+key, value, next)
		}
	case []any:
		got, ok := actual.([]any)
		if !ok || len(got) != len(want) {
			t.Fatalf("%s length/type mismatch", path)
		}
		for i, value := range want {
			compareATS(t, fmt.Sprintf("%s[%d]", path, i), value, got[i])
		}
	case float64:
		got, ok := actual.(float64)
		if !ok || math.Abs(got-want) > 1e-8*math.Max(1, math.Abs(want)) {
			t.Fatalf("%s got %v want %.12g", path, actual, want)
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s got %v want %v", path, actual, expected)
		}
	}
}
func validRequest() Request {
	bars := make([]foundation.KLine, 30)
	for i := range bars {
		bars[i] = foundation.KLine{Symbol: "600733.SH", Time: time.Date(2025, 1, i+1, 0, 0, 0, 0, time.UTC), Open: 10, High: 11, Low: 9, Close: 10, Volume: 1000}
	}
	return Request{Symbol: "600733.SH", Period: "day", Bars: bars}
}
func TestValidationAndCancellation(t *testing.T) {
	for _, name := range []string{"short", "too-many", "period", "duplicate", "reversed", "zero-time", "negative-volume", "zero-price", "bounds", "symbol", "nan", "inf"} {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			switch name {
			case "short":
				r.Bars = r.Bars[:9]
			case "too-many":
				r.Bars = make([]foundation.KLine, 1001)
			case "period":
				r.Period = "1"
			case "duplicate":
				r.Bars[1].Time = r.Bars[0].Time
			case "reversed":
				r.Bars[0], r.Bars[1] = r.Bars[1], r.Bars[0]
			case "zero-time":
				r.Bars[0].Time = time.Time{}
			case "negative-volume":
				r.Bars[0].Volume = -1
			case "zero-price":
				r.Bars[0].Close = 0
			case "bounds":
				r.Bars[0].High = 8
			case "symbol":
				r.Bars[0].Symbol = "000001.SZ"
			case "nan":
				r.Bars[0].Close = math.NaN()
			case "inf":
				r.Bars[0].High = math.Inf(1)
			}
			if !errors.Is(Validate(r), ErrInvalidInput) {
				t.Fatal("invalid request accepted")
			}
		})
	}
	if _, err := Analyze(context.Background(), []byte(`{"bars":`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestNativeRuntimeDoesNotRequirePython(t *testing.T) {
	t.Setenv("SBC_PYTHON", "unavailable-python")
	t.Setenv("PATH", "")
	input, _ := json.Marshal(validRequest())
	output, err := Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(output, &result)
	if result["implementation"] != "go-native" {
		t.Fatal("non-native runtime")
	}
}
func TestConcurrentSnapshots(t *testing.T) {
	input, _ := json.Marshal(validRequest())
	expected, err := Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			actual, err := Analyze(context.Background(), input)
			if err != nil || string(actual) != string(expected) {
				t.Fatalf("concurrent result differs: %v", err)
			}
		})
	}
}
