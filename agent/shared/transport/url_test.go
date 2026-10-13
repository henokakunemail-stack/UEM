package transport

import "testing"

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"uem.esta.co.id", "https://uem.esta.co.id"},
		{"uem.esta.co.id/", "https://uem.esta.co.id"},
		{"https://uem.esta.co.id", "https://uem.esta.co.id"},
		{"https://uem.esta.co.id/", "https://uem.esta.co.id"},
		{"http://uem.esta.co.id", "http://uem.esta.co.id"},
		{"localhost:8443", "http://localhost:8443"},
		{"127.0.0.1:8443", "http://127.0.0.1:8443"},
		{"192.168.1.50:8080", "http://192.168.1.50:8080"},
		{"10.0.0.1:8080", "http://10.0.0.1:8080"},
		{"ws://uem.esta.co.id", "ws://uem.esta.co.id"},
		{"wss://uem.esta.co.id", "wss://uem.esta.co.id"},
	}

	for _, tt := range tests {
		got := NormalizeServerURL(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeServerURL(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestWsURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://uem.esta.co.id", "wss://uem.esta.co.id"},
		{"http://uem.esta.co.id", "ws://uem.esta.co.id"},
		{"uem.esta.co.id", "wss://uem.esta.co.id"},
		{"wss://uem.esta.co.id", "wss://uem.esta.co.id"},
		{"ws://uem.esta.co.id", "ws://uem.esta.co.id"},
		{"localhost:8443", "ws://localhost:8443"},
	}

	for _, tt := range tests {
		got := wsURL(tt.input)
		if got != tt.expected {
			t.Errorf("wsURL(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}
