package tdx

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func minuteFixture() []byte {
	b := make([]byte, 6)
	le.PutUint16(b, 1)
	le.PutUint16(b[2:], uint16((2026-2004)<<11|1009))
	le.PutUint16(b[4:], 571)
	b = append(b, 0x90, 0x9c, 0x01, 0, 0, 0)
	v := make([]byte, 8)
	le.PutUint32(v, 0x43800000)
	le.PutUint32(v[4:], 0x45200000)
	return append(b, v...)
}
func TestMinuteVolumeUnitsAndDate(t *testing.T) {
	for _, cat := range []int{0, 8} {
		bars, e := decodeBars(minuteFixture(), cat, "600733.SH", time.Now())
		if e != nil {
			t.Fatal(e)
		}
		b := bars[0]
		if b.Volume != 256 || b.Amount != 2560 || b.Close != 10 || b.Time.Format("2006-01-02 15:04") != "2026-10-09 09:31" {
			t.Fatalf("%+v", b)
		}
	}
}
func TestFragmentedCompressedResponse(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	payload := bytes.Repeat([]byte("TDX"), 80)
	go func() {
		defer right.Close()
		req := make([]byte, 1)
		io.ReadFull(right, req)
		var packed bytes.Buffer
		z := zlib.NewWriter(&packed)
		z.Write(payload)
		z.Close()
		head := make([]byte, 16)
		le.PutUint16(head[12:], uint16(packed.Len()))
		le.PutUint16(head[14:], uint16(len(payload)))
		for _, v := range append(head, packed.Bytes()...) {
			right.Write([]byte{v})
		}
	}()
	left.SetDeadline(time.Now().Add(time.Second))
	got, e := exchange(left, []byte{1})
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatal(e)
	}
}
func TestNativeSocketHandshakeCacheAndCancellation(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		for n := 0; n < 6; n++ {
			head := make([]byte, 10)
			if _, e = io.ReadFull(conn, head); e != nil {
				return
			}
			request := make([]byte, int(le.Uint16(head[6:])))
			if _, e = io.ReadFull(conn, request); e != nil {
				return
			}
			body := []byte{1}
			if n == 3 {
				body = minuteFixture()
			}
			if n == 4 {
				body = []byte{0, 0}
			}
			if n == 5 {
				body = make([]byte, 25)
			}
			reply := make([]byte, 16)
			le.PutUint16(reply[12:], uint16(len(body)))
			le.PutUint16(reply[14:], uint16(len(body)))
			conn.Write(append(reply, body...))
		}
	}()
	c, e := New(filepath.Join(t.TempDir(), "tdx.db"), []string{listener.Addr().String()})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	rows, e := c.KLine(context.Background(), "600733.SH", "1", 1)
	if e != nil || len(rows) != 1 || rows[0].Volume != 256 {
		t.Fatal(e, rows)
	}
	<-done
	// Cache works without another connection, including restart of the client.
	got, e := c.KLine(context.Background(), "600733.SH", "1", 1)
	if e != nil || len(got) != 1 {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.KLine(ctx, "000001.SZ", "1", 1); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestEmptyDailyResponseDoesNotTriggerReconnectCooldown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		for connection := 0; connection < 2; connection++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				done <- acceptErr
				return
			}
			for requestIndex := 0; requestIndex < 5; requestIndex++ {
				head := make([]byte, 10)
				if _, readErr := io.ReadFull(conn, head); readErr != nil {
					conn.Close()
					done <- readErr
					return
				}
				request := make([]byte, int(le.Uint16(head[6:])))
				if _, readErr := io.ReadFull(conn, request); readErr != nil {
					conn.Close()
					done <- readErr
					return
				}
				body := []byte{1}
				if requestIndex == 3 {
					body = []byte{0, 0}
				}
				reply := make([]byte, 16)
				le.PutUint16(reply[12:], uint16(len(body)))
				le.PutUint16(reply[14:], uint16(len(body)))
				if _, writeErr := conn.Write(append(reply, body...)); writeErr != nil {
					conn.Close()
					done <- writeErr
					return
				}
			}
			conn.Close()
		}
		done <- nil
	}()

	client, err := New(filepath.Join(t.TempDir(), "tdx.db"), []string{listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, symbol := range []string{"920305.BJ", "920305.BJ", "600733.SH"} {
		bars, fetchErr := client.KLine(context.Background(), symbol, "day", 1)
		if fetchErr != nil || len(bars) != 0 {
			t.Fatalf("empty series for %s: bars=%d err=%v", symbol, len(bars), fetchErr)
		}
	}
	if !client.retryAt.IsZero() {
		t.Fatalf("empty series triggered cooldown until %s", client.retryAt)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInvalidWireBoundaries(t *testing.T) {
	for _, s := range []string{"", "0100", "2103"} {
		b, _ := hex.DecodeString(s)
		if _, e := decodeBars(b, 8, "600733.SH", time.Now()); e == nil {
			t.Fatal(s)
		}
	}
}
