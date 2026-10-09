package tdx

// Wire format follows the ATS pytdx GetSecurityBarsCmd/SetupCmd parsers.
// This package intentionally implements A-share bars only, not trading commands.
import (
	"bytes"
	"compress/zlib"
	"easy-stock/backend/internal/foundation"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net"
	"time"
)

var le = binary.LittleEndian
var zone = time.FixedZone("Asia/Shanghai", 8*3600)
var setup = []string{"0c0218930001030003000d0001", "0c0218940001030003000d0002", "0c031899000120002000db0fd5d0c9ccd6a4a8af0000008fc22540130000d500c9ccbdf0d7ea00000002"}

func exchange(conn net.Conn, request []byte) ([]byte, error) {
	for len(request) > 0 {
		n, err := conn.Write(request)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.ErrShortWrite
		}
		request = request[n:]
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	packed, unpacked := int(le.Uint16(header[12:])), int(le.Uint16(header[14:]))
	if packed == 0 || unpacked == 0 {
		return nil, fmt.Errorf("empty TDX response")
	}
	body := make([]byte, packed)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	if packed != unpacked {
		r, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		body, err = io.ReadAll(io.LimitReader(r, int64(unpacked)+1))
		if err != nil {
			return nil, err
		}
	}
	if len(body) != unpacked {
		return nil, fmt.Errorf("TDX response length mismatch")
	}
	return body, nil
}
func barRequest(category, market, start, count int, code string) []byte {
	b, _ := hex.DecodeString("0c01086401011c001c002d0500003030303030310900010000000a0000000000000000000000")
	le.PutUint16(b[12:], uint16(market))
	copy(b[14:20], code)
	le.PutUint16(b[20:], uint16(category))
	le.PutUint16(b[24:], uint16(start))
	le.PutUint16(b[26:], uint16(count))
	return b
}
func price(b []byte, pos *int) (int64, error) {
	if *pos >= len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := b[*pos]
	*pos++
	n := int64(v & 63)
	negative := v&64 != 0
	for shift := uint(6); v&128 != 0; shift += 7 {
		if *pos >= len(b) || shift > 55 {
			return 0, fmt.Errorf("invalid TDX price")
		}
		v = b[*pos]
		*pos++
		n |= int64(v&127) << shift
	}
	if negative {
		n = -n
	}
	return n, nil
}

// TDX's custom volume encoding is not IEEE float32.
func volume(v uint32) float64 {
	e := int(v>>24) * 2
	h := int((v >> 16) & 255)
	m := float64((v >> 8) & 255)
	l := float64(v & 255)
	a := math.Pow(2, float64(e-127))
	var b float64
	if h > 128 {
		b = math.Pow(2, float64(e-134))*128 + float64(h&127)*math.Pow(2, float64(e-133))
	} else if e-134 >= 0 {
		b = math.Pow(2, float64(e-134)) * float64(h)
	} else {
		b = math.Pow(2, float64(134-e)) * float64(h)
	}
	c, d := math.Pow(2, float64(e-142))*m, math.Pow(2, float64(e-150))*l
	if h&128 != 0 {
		c *= 2
		d *= 2
	}
	if v == 0 {
		return 0
	}
	return a + b + c + d
}
func decodeBars(body []byte, category int, symbol string, now time.Time) ([]foundation.KLine, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	count := int(le.Uint16(body))
	if count > 800 {
		return nil, fmt.Errorf("TDX page exceeds 800 bars")
	}
	out := make([]foundation.KLine, 0, count)
	pos := 2
	var base int64
	for i := 0; i < count; i++ {
		if pos+4 > len(body) {
			return nil, io.ErrUnexpectedEOF
		}
		var y, m, d, h, min int
		if category < 4 || category == 7 || category == 8 {
			date := int(le.Uint16(body[pos:]))
			minutes := int(le.Uint16(body[pos+2:]))
			y = (date >> 11) + 2004
			m = (date % 2048) / 100
			d = (date % 2048) % 100
			h = minutes / 60
			min = minutes % 60
		} else {
			date := int(le.Uint32(body[pos:]))
			y = date / 10000
			m = date % 10000 / 100
			d = date % 100
			h = 15
		}
		pos += 4
		diffs := [4]int64{}
		for j := range diffs {
			v, err := price(body, &pos)
			if err != nil {
				return nil, err
			}
			diffs[j] = v
		}
		if pos+8 > len(body) {
			return nil, io.ErrUnexpectedEOF
		}
		vol, amount := volume(le.Uint32(body[pos:])), volume(le.Uint32(body[pos+4:]))
		pos += 8
		o := base + diffs[0]
		c := o + diffs[1]
		hi, lo := o+diffs[2], o+diffs[3]
		base = c
		t := time.Date(y, time.Month(m), d, h, min, 0, 0, zone)
		if y < 2004 || y > now.Year()+1 || t.After(now.Add(24*time.Hour)) || int(t.Month()) != m || t.Day() != d || h > 23 || min > 59 || lo <= 0 || hi < o || hi < c || lo > o || lo > c || !finite(vol) || !finite(amount) || vol < 0 || amount < 0 {
			return nil, fmt.Errorf("invalid TDX bar %d", i)
		}
		// ATS minute bars (categories 0..3,7,8) are shares; daily/weekly/monthly are hands.
		units := 100.0
		if category < 4 || category == 7 || category == 8 {
			units = 1
		}
		out = append(out, foundation.KLine{Symbol: symbol, Time: t, Open: float64(o) / 1000, Close: float64(c) / 1000, High: float64(hi) / 1000, Low: float64(lo) / 1000, Volume: vol * units, Amount: amount, Meta: foundation.SourceMeta{Source: "tdx", FetchedAt: now, TradeDate: t.Format("2006-01-02"), AvailableFields: []string{"ohlc", "volume_shares", "amount_cny"}}})
	}
	if pos != len(body) {
		return nil, fmt.Errorf("unexpected TDX bar trailing bytes")
	}
	return out, nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
