package networkfilter

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	dohCanaryDomain = "use-application-dns.net"
	defaultUpstream = "1.1.1.1:53"
	dnsTimeout      = 2 * time.Second
)

// DNSProxy is a lightweight in-process DNS server for Whitelist default-deny enforcement.
// It intercepts queries on 127.0.0.1:53, returns 0.0.0.0 / :: for non-whitelisted domains,
// and forwards whitelisted domains and the management server host to upstream DNS.
type DNSProxy struct {
	mu             sync.RWMutex
	conn           *net.UDPConn
	allowedDomains []string
	managementHost string
	upstreamAddr   string
	cancel         context.CancelFunc
	done           chan struct{}
}

// StartDNSProxy initializes and binds UDP listener on 127.0.0.1:53.
func StartDNSProxy(listenAddr string, upstreamDNS string, allowed []string, serverURL string) (*DNSProxy, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:53"
	}
	if upstreamDNS == "" {
		upstreamDNS = defaultUpstream
	}
	if !strings.Contains(upstreamDNS, ":") {
		upstreamDNS = upstreamDNS + ":53"
	}

	mgmtHost := extractHostFromURL(serverURL)

	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve dns listen addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind dns proxy on %s: %w", listenAddr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	proxy := &DNSProxy{
		conn:           conn,
		allowedDomains: normalizePatterns(allowed),
		managementHost: strings.ToLower(mgmtHost),
		upstreamAddr:   upstreamDNS,
		cancel:         cancel,
		done:           make(chan struct{}),
	}

	go proxy.serve(ctx)
	log.Info().Str("listen", listenAddr).Str("upstream", upstreamDNS).
		Int("allowed_count", len(allowed)).Str("mgmt_host", mgmtHost).
		Msg("whitelist DNS proxy started")

	return proxy, nil
}

func (p *DNSProxy) Stop() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.mu.Unlock()

	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
	}
	log.Info().Msg("whitelist DNS proxy stopped")
}

func (p *DNSProxy) UpdateAllowed(allowed []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowedDomains = normalizePatterns(allowed)
}

func (p *DNSProxy) serve(ctx context.Context) {
	defer close(p.done)
	buf := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = p.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, remoteAddr, err := p.conn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-ctx.Done():
				return
			default:
				log.Debug().Err(err).Msg("dns read packet error")
				continue
			}
		}

		if n < 12 {
			continue // invalid DNS header
		}

		reqData := make([]byte, n)
		copy(reqData, buf[:n])

		go p.handlePacket(reqData, remoteAddr)
	}
}

func (p *DNSProxy) handlePacket(req []byte, remoteAddr net.Addr) {
	domain, qtype, qclass, qEnd, err := parseDNSQuestion(req)
	if err != nil {
		return
	}

	lowerDomain := strings.ToLower(strings.TrimSuffix(domain, "."))

	// 1. Canary domain for DoH disablement (RFC/Browser standard).
	// Returning NXDOMAIN forces Firefox and Chrome to disable DoH and use system resolver.
	if lowerDomain == dohCanaryDomain {
		resp := buildNXDOMAIN(req, qEnd)
		if resp != nil {
			_, _ = p.conn.WriteTo(resp, remoteAddr)
		}
		return
	}

	// 2. Check if domain is allowed.
	if p.isAllowed(lowerDomain) {
		resp, err := forwardDNS(req, p.upstreamAddr)
		if err == nil && len(resp) > 0 {
			_, _ = p.conn.WriteTo(resp, remoteAddr)
			return
		}
	}

	// 3. Domain is blocked under Whitelist mode: sinkhole with 0.0.0.0 / ::
	resp := buildSinkholeResponse(req, lowerDomain, qtype, qclass, qEnd)
	if resp != nil {
		_, _ = p.conn.WriteTo(resp, remoteAddr)
	}
}

func (p *DNSProxy) isAllowed(domain string) bool {
	// Always allow management server host and local names
	if domain == "" || domain == "localhost" || domain == "127.0.0.1" || domain == "::1" {
		return true
	}

	p.mu.RLock()
	mgmt := p.managementHost
	allowed := p.allowedDomains
	p.mu.RUnlock()

	if mgmt != "" && (domain == mgmt || strings.HasSuffix(domain, "."+mgmt)) {
		return true
	}

	for _, pattern := range allowed {
		if pattern == "*" || pattern == domain {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(domain, suffix) {
				return true
			}
		}
		// Match domain and its subdomains (e.g. pattern "google.com" matches "mail.google.com")
		if strings.HasSuffix(domain, "."+pattern) {
			return true
		}
	}
	return false
}

func forwardDNS(req []byte, upstream string) ([]byte, error) {
	conn, err := net.DialTimeout("udp", upstream, dnsTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(dnsTimeout))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	resp := make([]byte, 4096)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, err
	}
	return resp[:n], nil
}

// parseDNSQuestion parses the question section of a standard RFC 1035 DNS packet.
func parseDNSQuestion(data []byte) (name string, qtype, qclass uint16, qEnd int, err error) {
	if len(data) < 12 {
		return "", 0, 0, 0, fmt.Errorf("packet too short")
	}

	qdCount := binary.BigEndian.Uint16(data[4:6])
	if qdCount == 0 {
		return "", 0, 0, 0, fmt.Errorf("no questions")
	}

	offset := 12
	var labels []string

	for offset < len(data) {
		length := int(data[offset])
		offset++
		if length == 0 {
			break
		}
		if (length & 0xc0) == 0xc0 { // pointer
			offset++
			break
		}
		if offset+length > len(data) {
			return "", 0, 0, 0, fmt.Errorf("invalid label length")
		}
		labels = append(labels, string(data[offset:offset+length]))
		offset += length
	}

	if offset+4 > len(data) {
		return "", 0, 0, 0, fmt.Errorf("question truncated")
	}

	qtype = binary.BigEndian.Uint16(data[offset : offset+2])
	qclass = binary.BigEndian.Uint16(data[offset+2 : offset+4])
	offset += 4

	return strings.Join(labels, "."), qtype, qclass, offset, nil
}

// buildSinkholeResponse creates an RFC 1035 response pointing A queries to 0.0.0.0 and AAAA to ::.
func buildSinkholeResponse(req []byte, _ string, qtype, qclass uint16, qEnd int) []byte {
	if len(req) < 12 || qEnd > len(req) {
		return nil
	}

	resp := make([]byte, 0, qEnd+32)
	// 1. Copy Header
	header := make([]byte, 12)
	copy(header[:2], req[:2]) // ID

	// Flags: QR=1 (resp), Opcode=0, AA=1 (authoritative), RD=req RD, RA=1, RCODE=0
	rd := req[2] & 0x01
	header[2] = 0x84 | rd // QR=1, AA=1, RD
	header[3] = 0x80      // RA=1, RCODE=0

	header[4] = 0x00 // QDCOUNT = 1
	header[5] = 0x01

	switch qtype {
	case 1, 28: // Type A or AAAA: 1 Answer
		header[6] = 0x00
		header[7] = 0x01
	default: // Other types: 0 answers (NODATA)
		header[6] = 0x00
		header[7] = 0x00
	}

	resp = append(resp, header...)
	// 2. Copy Question
	resp = append(resp, req[12:qEnd]...)

	// 3. Answer Section
	if qtype == 1 { // A Record -> 0.0.0.0
		answer := []byte{
			0xc0, 0x0c, // Pointer to domain in question
			0x00, 0x01, // Type A
			0x00, 0x01, // Class IN
			0x00, 0x00, 0x00, 0x3c, // TTL 60s
			0x00, 0x04, // Data Length = 4
			0x00, 0x00, 0x00, 0x00, // 0.0.0.0
		}
		resp = append(resp, answer...)
	} else if qtype == 28 { // AAAA Record -> ::
		answer := []byte{
			0xc0, 0x0c, // Pointer to domain in question
			0x00, 0x1c, // Type AAAA
			0x00, 0x01, // Class IN
			0x00, 0x00, 0x00, 0x3c, // TTL 60s
			0x00, 0x10, // Data Length = 16
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // ::
		}
		resp = append(resp, answer...)
	}

	return resp
}

// buildNXDOMAIN constructs an authoritative NXDOMAIN response for DoH canary query.
func buildNXDOMAIN(req []byte, qEnd int) []byte {
	if len(req) < 12 || qEnd > len(req) {
		return nil
	}

	resp := make([]byte, 0, qEnd)
	header := make([]byte, 12)
	copy(header[:2], req[:2]) // ID

	rd := req[2] & 0x01
	header[2] = 0x84 | rd // QR=1, AA=1, RD
	header[3] = 0x83      // RA=1, RCODE=3 (NXDOMAIN)

	header[4] = 0x00 // QDCOUNT = 1
	header[5] = 0x01

	resp = append(resp, header...)
	resp = append(resp, req[12:qEnd]...)
	return resp
}

func extractHostFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	return host
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		norm := strings.ToLower(strings.TrimSpace(p))
		norm = strings.TrimPrefix(norm, "http://")
		norm = strings.TrimPrefix(norm, "https://")
		if idx := strings.Index(norm, "/"); idx != -1 {
			norm = norm[:idx]
		}
		if idx := strings.Index(norm, ":"); idx != -1 {
			norm = norm[:idx]
		}
		norm = strings.TrimSuffix(norm, ".")
		if norm != "" && !strings.HasPrefix(norm, "#") {
			out = append(out, norm)
		}
	}
	return out
}
