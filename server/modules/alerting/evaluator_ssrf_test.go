package alerting

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestIsPrivateOrBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"::1", true},
		// Private RFC 1918
		{"10.0.0.1", true},
		{"10.254.1.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.100", true},
		// Link-Local / Cloud metadata RFC 3927
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"fe80::1", true},
		// CGNAT RFC 6598
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		// Zero / Unspecified
		{"0.0.0.0", true},
		{"::", true},
		// Multicast
		{"224.0.0.1", true},
		{"ff02::1", true},
		// Public IPs
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %s", tc.ip)
		}
		got := isPrivateOrBlockedIP(ip)
		if got != tc.blocked {
			t.Errorf("isPrivateOrBlockedIP(%s) = %v, want %v", tc.ip, got, tc.blocked)
		}
	}
}

func TestSafeWebhookClientRejectsPrivateIP(t *testing.T) {
	client := newSafeWebhookClient()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:9999/webhook", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected request to 127.0.0.1 to be blocked by safe transport")
	}
	if !strings.Contains(err.Error(), "blocked/private IP") {
		t.Errorf("expected error to mention blocked/private IP, got: %v", err)
	}
}
