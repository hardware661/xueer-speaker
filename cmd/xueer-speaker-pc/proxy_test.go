//go:build windows

package main

import "testing"

func TestParseProxy(t *testing.T) {
	for _, c := range []struct{ in, scheme, want string }{
		{"127.0.0.1:7890", "https", "http://127.0.0.1:7890"},
		{"http=127.0.0.1:1;https=127.0.0.1:2", "https", "http://127.0.0.1:2"},
		{"socks=127.0.0.1:3", "https", "socks5://127.0.0.1:3"},
		{"", "https", ""},
	} {
		u, _ := parseProxy(c.in, c.scheme)
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != c.want {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}
