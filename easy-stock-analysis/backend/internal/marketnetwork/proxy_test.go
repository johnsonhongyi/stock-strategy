package marketnetwork

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestProxyConfiguration(t *testing.T) {
	c := &Config{Path: filepath.Join(t.TempDir(), "market-network.json")}
	tr := c.Transport()
	req, _ := http.NewRequest("GET", "https://api.kraken.com/0/public/OHLC", nil)
	proxy, err := tr.Proxy(req)
	if err != nil || proxy != nil {
		t.Fatalf("default proxy=%v err=%v", proxy, err)
	}
	for _, value := range []string{"http://127.0.0.1:7890", "https://proxy.example:443", ""} {
		if err := c.Save(Settings{ProxyURL: value}); err != nil {
			t.Fatal(err)
		}
		proxy, err = tr.Proxy(req)
		if err != nil {
			t.Fatal(err)
		}
		if value == "" {
			if proxy != nil {
				t.Fatal("direct mode uses proxy")
			}
		} else if proxy == nil || proxy.String() != value {
			t.Fatalf("proxy=%v", proxy)
		}
	}
	if err := c.Save(Settings{ProxyURL: "http://proxy.example:7890"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://localhost:20081/api/health", "http://127.0.0.1:20081/api/health", "http://[::1]:20081/api/health"} {
		local, _ := http.NewRequest("GET", target, nil)
		proxy, err = tr.Proxy(local)
		if err != nil || proxy != nil {
			t.Fatalf("loopback proxied: %v %v", proxy, err)
		}
	}
	for _, value := range []string{"socks5://host:1", "http://user:secret@host:7890", "http://host:99999", "http://host/path", "http://host/?token=secret"} {
		if c.Save(Settings{ProxyURL: value}) == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	saved, err := c.Load()
	if err != nil || saved.ProxyURL != "http://proxy.example:7890" {
		t.Fatalf("bad save overwrote settings: %+v %v", saved, err)
	}
}
