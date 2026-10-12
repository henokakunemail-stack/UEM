package networkfilter

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func buildQueryPacket(id uint16, domain string, qtype uint16) []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[0:2], id)
	packet[2] = 0x01 // RD = 1
	packet[3] = 0x00
	binary.BigEndian.PutUint16(packet[4:6], 1) // QDCOUNT = 1

	labels := splitDomainLabels(domain)
	for _, l := range labels {
		packet = append(packet, byte(len(l)))
		packet = append(packet, []byte(l)...)
	}
	packet = append(packet, 0) // terminating null byte

	qtypeBytes := make([]byte, 4)
	binary.BigEndian.PutUint16(qtypeBytes[0:2], qtype)
	binary.BigEndian.PutUint16(qtypeBytes[2:4], 1) // QCLASS = IN (1)
	packet = append(packet, qtypeBytes...)

	return packet
}

func splitDomainLabels(d string) []string {
	var labels []string
	start := 0
	for i := 0; i < len(d); i++ {
		if d[i] == '.' {
			if i > start {
				labels = append(labels, d[start:i])
			}
			start = i + 1
		}
	}
	if start < len(d) {
		labels = append(labels, d[start:])
	}
	return labels
}

func TestDNSProxyQuestionParser(t *testing.T) {
	req := buildQueryPacket(0x1234, "detik.com", 1)
	domain, qtype, qclass, qEnd, err := parseDNSQuestion(req)
	if err != nil {
		t.Fatalf("parseDNSQuestion failed: %v", err)
	}
	if domain != "detik.com" {
		t.Errorf("domain = %q, want detik.com", domain)
	}
	if qtype != 1 {
		t.Errorf("qtype = %d, want 1", qtype)
	}
	if qclass != 1 {
		t.Errorf("qclass = %d, want 1", qclass)
	}
	if qEnd != len(req) {
		t.Errorf("qEnd = %d, want %d", qEnd, len(req))
	}
}

func TestDNSProxyAllowedMatching(t *testing.T) {
	proxy := &DNSProxy{
		allowedDomains: []string{"google.com", "*.internal.org"},
		managementHost: "mgmt.company.local",
	}

	tests := []struct {
		domain string
		want   bool
	}{
		{"google.com", true},
		{"www.google.com", true},
		{"mail.google.com", true},
		{"notgoogle.com", false},
		{"google.com.attacker.com", false},
		{"api.internal.org", true},
		{"internal.org", false},
		{"mgmt.company.local", true},
		{"sub.mgmt.company.local", true},
		{"detik.com", false},
		{"localhost", true},
	}

	for _, tc := range tests {
		got := proxy.isAllowed(tc.domain)
		if got != tc.want {
			t.Errorf("isAllowed(%q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}

func TestDNSProxySinkholeResponse(t *testing.T) {
	req := buildQueryPacket(0x5678, "blocked.com", 1)
	_, qtype, qclass, qEnd, _ := parseDNSQuestion(req)

	resp := buildSinkholeResponse(req, "blocked.com", qtype, qclass, qEnd)
	if len(resp) < 12 {
		t.Fatalf("sinkhole response too short: %d", len(resp))
	}

	id := binary.BigEndian.Uint16(resp[0:2])
	if id != 0x5678 {
		t.Errorf("id = 0x%x, want 0x5678", id)
	}

	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount != 1 {
		t.Fatalf("ancount = %d, want 1", ancount)
	}

	// Last 4 bytes of A record must be 0.0.0.0
	rdata := resp[len(resp)-4:]
	if rdata[0] != 0 || rdata[1] != 0 || rdata[2] != 0 || rdata[3] != 0 {
		t.Errorf("rdata = %v, want 0.0.0.0", rdata)
	}
}

func TestDNSProxyDoHCanaryReturnsNXDOMAIN(t *testing.T) {
	req := buildQueryPacket(0x9abc, "use-application-dns.net", 1)
	_, _, _, qEnd, _ := parseDNSQuestion(req)

	resp := buildNXDOMAIN(req, qEnd)
	if len(resp) < 12 {
		t.Fatalf("NXDOMAIN response too short: %d", len(resp))
	}

	rcode := resp[3] & 0x0f
	if rcode != 3 {
		t.Errorf("rcode = %d, want 3 (NXDOMAIN)", rcode)
	}
}

func TestDNSProxyEndToEndLoopback(t *testing.T) {
	// Start DNSProxy on random loopback UDP port
	proxy, err := StartDNSProxy("127.0.0.1:0", "1.1.1.1:53", []string{"allowed.org"}, "https://central-server.local:8080")
	if err != nil {
		t.Fatalf("start dns proxy: %v", err)
	}
	defer proxy.Stop()

	listenAddr := proxy.conn.LocalAddr().String()

	conn, err := net.Dial("udp", listenAddr)
	if err != nil {
		t.Fatalf("dial dns proxy: %v", err)
	}
	defer conn.Close()

	// Query for blocked domain
	query := buildQueryPacket(0x4321, "detik.com", 1)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("write query: %v", err)
	}

	resp := make([]byte, 512)
	n, err := conn.Read(resp)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	resp = resp[:n]
	id := binary.BigEndian.Uint16(resp[0:2])
	if id != 0x4321 {
		t.Errorf("response ID = 0x%x, want 0x4321", id)
	}

	// Check that answer IP is 0.0.0.0
	rdata := resp[len(resp)-4:]
	if rdata[0] != 0 || rdata[1] != 0 || rdata[2] != 0 || rdata[3] != 0 {
		t.Errorf("got IP %v, want 0.0.0.0 for blocked domain", rdata)
	}
}
