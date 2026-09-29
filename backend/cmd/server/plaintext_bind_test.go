package main

import "testing"

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":   true,
		"localhost:8080":   true,
		"[::1]:8080":       true,
		":8080":            false, // every interface
		"0.0.0.0:8080":     false,
		"":                 false,
		"192.168.1.5:8080": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Plaintext behind an https public URL is an edge-terminating proxy, where
// kubectl works; plaintext behind an http one is where it does not.
func TestBehindTLSProxy(t *testing.T) {
	cases := map[string]bool{
		"https://kubemg.example.com":      true,
		"HTTPS://kubemg.example.com":      true,
		" https://kubemg.example.com:443": true,
		"http://localhost:8080":           false,
		"":                                false,
	}
	for url, want := range cases {
		if got := behindTLSProxy(url); got != want {
			t.Errorf("behindTLSProxy(%q) = %v, want %v", url, got, want)
		}
	}
}
