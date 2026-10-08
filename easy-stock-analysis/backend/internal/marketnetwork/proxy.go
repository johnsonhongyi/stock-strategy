package marketnetwork

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Settings struct {
	ProxyURL string `json:"proxy_url"`
}
type Config struct {
	Path string
	mu   sync.Mutex
}

func (c *Config) Load() (Settings, error) {
	var settings Settings
	body, err := os.ReadFile(c.Path)
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("读取行情代理配置失败")
	}
	if json.Unmarshal(body, &settings) != nil {
		return settings, fmt.Errorf("行情代理配置格式无效")
	}
	return settings, nil
}
func Validate(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("代理须为 http://主机:端口 或 https://主机:端口，不支持凭据或路径")
	}
	if _, err := net.LookupPort("tcp", u.Port()); u.Port() != "" && err != nil {
		return fmt.Errorf("代理端口无效")
	}
	return nil
}
func (c *Config) Save(settings Settings) error {
	settings.ProxyURL = strings.TrimSpace(settings.ProxyURL)
	if err := Validate(settings.ProxyURL); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.Path), 0700); err != nil {
		return fmt.Errorf("创建代理配置目录失败")
	}
	f, err := os.CreateTemp(filepath.Dir(c.Path), ".market-network-*")
	if err != nil {
		return fmt.Errorf("创建代理配置失败")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(settings); err != nil {
		f.Close()
		return fmt.Errorf("写入代理配置失败")
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("关闭代理配置失败")
	}
	if err = os.Rename(name, c.Path); err != nil {
		return fmt.Errorf("保存代理配置失败")
	}
	return nil
}
func (c *Config) Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		host := r.URL.Hostname()
		if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			return nil, nil
		}
		settings, err := c.Load()
		if err != nil {
			return nil, err
		}
		if settings.ProxyURL == "" {
			return nil, nil
		}
		if err := Validate(settings.ProxyURL); err != nil {
			return nil, err
		}
		return url.Parse(settings.ProxyURL)
	}
	return t
}
