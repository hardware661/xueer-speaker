//go:build windows

package main

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxy follows the Windows proxy setting (Settings → Network → Proxy)
// like browsers do: a server may only let some networks reach /pc/,
// and the PC reaches it through that proxy. Read per connection,
// so turning the proxy on or off applies at the next reconnect.
func systemProxy(r *http.Request) (*url.URL, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return nil, nil
	}
	return parseProxy(server, r.URL.Scheme)
}

// parseProxy reads "host:port" or "http=h:p;https=h:p;socks=h:p".
func parseProxy(server, scheme string) (*url.URL, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, nil
	}
	if !strings.Contains(server, "=") {
		return url.Parse("http://" + server)
	}
	byScheme := map[string]string{}
	for _, part := range strings.Split(server, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			byScheme[strings.ToLower(k)] = v
		}
	}
	if v := byScheme[scheme]; v != "" {
		return url.Parse("http://" + v)
	}
	if v := byScheme["socks"]; v != "" {
		return url.Parse("socks5://" + v)
	}
	return nil, nil
}
