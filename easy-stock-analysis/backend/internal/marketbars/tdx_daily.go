package marketbars

import (
    "bufio"
    "context"
    "fmt"
    "io"
    "math"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"

    "easy-stock/backend/internal/foundation"
)

var tdxDailyFilePattern = regexp.MustCompile(`(?i)^(SH|SZ|BJ)([0-9]{6})\.TXT$`)
var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type TDXDailyCoverage struct {
    Codes        int64  `json:"codes"`
    FreshCodes   int64  `json:"fresh_codes"`
    StaleCodes   int64  `json:"stale_codes"`
    InvalidCodes int64  `json:"invalid_codes"`
    LatestDate   string `json:"latest_date,omitempty"`
}

func (s *Store) SetTDXDailyDirectory(directory string) {
    if s == nil { return }
    s.tdxSummaryMu.Lock()
    s.tdxDir = strings.TrimSpace(directory)
    s.tdxSummaryCheckedAt = time.Time{}
    s.tdxSummaryMu.Unlock()
}

func (s *Store) TDXDailyEnabled() bool {
    return s != nil && strings.TrimSpace(s.tdxDir) != ""
}

func (s *Store) TDXDailyCoverage(ctx context.Context, expectedDate string) (TDXDailyCoverage, error) {
    var result TDXDailyCoverage
    if !s.TDXDailyEnabled() { return result, nil }
    s.tdxSummaryMu.Lock()
    defer s.tdxSummaryMu.Unlock()
    if s.tdxSummaryExpected == expectedDate && time.Since(s.tdxSummaryCheckedAt) < 5*time.Minute {
        return s.tdxSummary, nil
    }
    entries, err := os.ReadDir(s.tdxDir)
    if err != nil { return result, fmt.Errorf("read TDX daily directory: %w", err) }
    for _, entry := range entries {
        if err := ctx.Err(); err != nil { return result, err }
        if entry.IsDir() || !tdxDailyFilePattern.MatchString(entry.Name()) { continue }
        result.Codes++
        match := tdxDailyFilePattern.FindStringSubmatch(entry.Name())
        latest, valid, err := readTDXDailyTail(filepath.Join(s.tdxDir, entry.Name()), match[2])
        if err != nil || !valid { result.InvalidCodes++; continue }
        if latest > result.LatestDate { result.LatestDate = latest }
        if latest == expectedDate { result.FreshCodes++ } else { result.StaleCodes++ }
    }
    s.tdxSummary = result
    s.tdxSummaryExpected = expectedDate
    s.tdxSummaryCheckedAt = time.Now()
    return result, nil
}

func (s *Store) readTDXDaily(ctx context.Context, symbol string, limit int) ([]foundation.KLine, bool, error) {
    code, _ := NormalizeSymbol(symbol, "CN")
    if !s.TDXDailyEnabled() || len(code) != 6 { return nil, false, nil }
    prefix := tdxExchangePrefix(code)
    explicit := strings.ToUpper(strings.TrimSpace(symbol))
    for _, exchange := range []string{"SH", "SZ", "BJ"} {
        if strings.HasSuffix(explicit, "."+exchange) || strings.HasPrefix(explicit, exchange) { prefix = exchange; break }
    }
    if prefix == "" { return nil, false, nil }
    path := filepath.Join(s.tdxDir, prefix+code+".TXT")
    file, err := os.Open(path)
    if os.IsNotExist(err) { return nil, false, nil }
    if err != nil { return nil, false, fmt.Errorf("open TDX daily file for %s: %w", code, err) }
    defer file.Close()
    info, err := file.Stat()
    if err != nil { return nil, false, err }
    fetchedAt := info.ModTime()
    scanner := bufio.NewScanner(file)
    scanner.Buffer(make([]byte, 4096), 4<<20)
    byDate := make(map[string]foundation.KLine)
    for scanner.Scan() {
        if err := ctx.Err(); err != nil { return nil, false, err }
        line, ok := parseTDXDailyLine(scanner.Text(), code)
        if ok {
            line.Meta.FetchedAt = fetchedAt
            byDate[line.Time.Format("2006-01-02")] = line
        }
    }
    if err := scanner.Err(); err != nil { return nil, false, fmt.Errorf("read TDX daily file for %s: %w", code, err) }
    lines := make([]foundation.KLine, 0, len(byDate))
    for _, line := range byDate { lines = append(lines, line) }
    sort.Slice(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
    for i := range lines {
        if i > 0 {
            lines[i].PreviousClose = lines[i-1].Close
            lines[i].ChangePercent = (lines[i].Close/lines[i-1].Close - 1) * 100
        }
    }
    if len(lines) == 0 { return nil, false, nil }
    if limit > 0 && len(lines) > limit { lines = lines[len(lines)-limit:] }
    return lines, true, nil
}

func readTDXDailyTail(path, code string) (string, bool, error) {
    file, err := os.Open(path)
    if err != nil { return "", false, err }
    defer file.Close()
    info, err := file.Stat()
    if err != nil { return "", false, err }
    size := info.Size()
    start := size - 4*1024
    if start < 0 { start = 0 }
    data, err := io.ReadAll(io.NewSectionReader(file, start, size-start))
    if err != nil { return "", false, err }
    lines := strings.Split(strings.TrimSpace(string(data)), "\n")
    for i := len(lines)-1; i >= 0; i-- {
        raw := strings.TrimSpace(lines[i])
        if raw == "" || !looksLikeTDXDailyRecord(raw) { continue }
        line, ok := parseTDXDailyLine(raw, code)
        if !ok { return "", false, nil }
        return line.Time.Format("2006-01-02"), true, nil
    }
    return "", false, nil
}

func looksLikeTDXDailyRecord(raw string) bool {
    first, _, _ := strings.Cut(raw, ",")
    if len(first) == 8 {
        for _, r := range first {
            if r < '0' || r > '9' { return false }
        }
        return true
    }
    return len(first) == 10 && first[4] == '-' && first[7] == '-'
}

func parseTDXDailyLine(raw, code string) (foundation.KLine, bool) {
    fields := strings.Split(strings.TrimSpace(raw), ",")
    if len(fields) < 7 { return foundation.KLine{}, false }
    date, ok := parseTDXDailyLineDate(raw)
    if !ok { return foundation.KLine{}, false }
    values := make([]float64, 6)
    for i := range values {
        var err error
        values[i], err = strconv.ParseFloat(strings.TrimSpace(fields[i+1]), 64)
        if err != nil || math.IsNaN(values[i]) || math.IsInf(values[i], 0) { return foundation.KLine{}, false }
    }
    line := foundation.KLine{
        Symbol: code, Time: time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, shanghai),
        Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4], Amount: values[5],
        Meta: foundation.SourceMeta{Source: "instock-tdx-daily", TradeDate: date.Format("2006-01-02")},
    }
    if !validDailyBar(line) { return foundation.KLine{}, false }
    return line, true
}

func parseTDXDailyLineDate(raw string) (time.Time, bool) {
    fields := strings.Split(strings.TrimSpace(raw), ",")
    if len(fields) < 7 { return time.Time{}, false }
    value := strings.TrimSpace(fields[0])
    for _, layout := range []string{"2006-01-02", "2006/01/02", "20060102"} {
        if parsed, err := time.ParseInLocation(layout, value, shanghai); err == nil {
            if parsed.After(time.Now().In(shanghai).AddDate(0, 0, 1)) { return time.Time{}, false }
            return parsed, true
        }
    }
    return time.Time{}, false
}

func tdxExchangePrefix(code string) string {
    if strings.HasPrefix(code, "92") { return "BJ" }
    switch code[0] {
    case '4', '8': return "BJ"
    case '5', '6', '9': return "SH"
    case '0', '1', '2', '3': return "SZ"
    default: return ""
    }
}
