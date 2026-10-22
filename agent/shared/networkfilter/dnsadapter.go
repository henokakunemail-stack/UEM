package networkfilter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/rs/zerolog/log"
)

// ErrLoopbackUnsupported means this OS has no way, in this agent, to send the
// system's DNS through the whitelist resolver. macOS used to return a no-op
// here and the whitelist reported "synced" while every lookup bypassed it.
var ErrLoopbackUnsupported = errors.New("networkfilter: this OS cannot route system DNS through the whitelist resolver")

// dnsBackup is what the system's DNS looked like before whitelist mode pointed
// it at 127.0.0.1. It is written to disk before anything changes: the backup
// used to live only in memory, so an agent that crashed or was killed left
// every adapter on 127.0.0.1 with nothing listening, and the next start backed
// up "127.0.0.1" (skipped as loopback) and "restored" the adapters to DHCP.
type dnsBackup struct {
	Method     string          `json:"method"` // windows | resolvectl | resolvconf
	Adapters   []adapterBackup `json:"adapters,omitempty"`
	Iface      string          `json:"iface,omitempty"`
	ResolvConf string          `json:"resolv_conf,omitempty"`
}

type adapterBackup struct {
	Name      string   `json:"name"`
	DHCP      bool     `json:"dhcp"`
	Addresses []string `json:"addresses,omitempty"`
}

// dnsConfigurator points the system resolver at the whitelist proxy and back.
// The engine snapshots, persists the snapshot, and only then changes anything.
type dnsConfigurator interface {
	Snapshot() (dnsBackup, error)
	// PointToLoopback returns a warning when it only partly succeeded (some
	// adapters refused), and an error when nothing was redirected.
	PointToLoopback(b dnsBackup) (warning string, err error)
	Restore(b dnsBackup) error
}

// systemDNS is the real configurator for the running OS.
type systemDNS struct{}

func (systemDNS) Snapshot() (dnsBackup, error) {
	switch runtime.GOOS {
	case "windows":
		adapters, err := getConnectedWindowsAdapters()
		if err != nil || len(adapters) == 0 {
			return dnsBackup{}, fmt.Errorf("no connected network adapter found: %v", err)
		}
		b := dnsBackup{Method: "windows"}
		for _, a := range adapters {
			b.Adapters = append(b.Adapters, getWindowsDNSBackup(a))
		}
		return b, nil
	case "linux":
		if _, err := exec.LookPath("resolvectl"); err == nil {
			if iface := getActiveLinuxInterface(); iface != "" {
				return dnsBackup{Method: "resolvectl", Iface: iface}, nil
			}
		}
		original, err := os.ReadFile("/etc/resolv.conf")
		if err != nil {
			return dnsBackup{}, fmt.Errorf("read /etc/resolv.conf: %w", err)
		}
		return dnsBackup{Method: "resolvconf", ResolvConf: string(original)}, nil
	}
	return dnsBackup{}, ErrLoopbackUnsupported
}

func (systemDNS) PointToLoopback(b dnsBackup) (string, error) {
	switch b.Method {
	case "windows":
		var refused []string
		for _, a := range b.Adapters {
			out, err := exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
				"name="+a.Name, "source=static", "address=127.0.0.1", "validate=no").CombinedOutput()
			if err != nil {
				refused = append(refused, fmt.Sprintf("%s (%v: %s)", a.Name, err, strings.TrimSpace(string(out))))
				continue
			}
			log.Info().Str("adapter", a.Name).Msg("configured adapter DNS to 127.0.0.1")
		}
		_ = exec.Command("ipconfig", "/flushdns").Run()
		// A refused adapter used to be logged and the whitelist reported as
		// enforced; lookups on that adapter bypass it entirely.
		if len(refused) == len(b.Adapters) {
			return "", fmt.Errorf("no adapter accepted 127.0.0.1 as its DNS server: %s", strings.Join(refused, "; "))
		}
		if len(refused) > 0 {
			return "whitelist not enforced on adapter(s): " + strings.Join(refused, "; "), nil
		}
		return "", nil
	case "resolvectl":
		if out, err := exec.Command("resolvectl", "dns", b.Iface, "127.0.0.1").CombinedOutput(); err != nil {
			return "", fmt.Errorf("resolvectl dns %s: %w (%s)", b.Iface, err, strings.TrimSpace(string(out)))
		}
		log.Info().Str("interface", b.Iface).Msg("configured Linux DNS via resolvectl")
		return "", nil
	case "resolvconf":
		if err := os.WriteFile("/etc/resolv.conf", []byte(rewriteResolvConf(b.ResolvConf)), 0o644); err != nil {
			return "", fmt.Errorf("write /etc/resolv.conf: %w", err)
		}
		log.Info().Msg("configured Linux DNS via /etc/resolv.conf")
		return "", nil
	}
	return "", ErrLoopbackUnsupported
}

func (systemDNS) Restore(b dnsBackup) error {
	switch b.Method {
	case "windows":
		var errs []string
		for _, a := range b.Adapters {
			var err error
			if a.DHCP || len(a.Addresses) == 0 {
				err = exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
					"name="+a.Name, "source=dhcp").Run()
			} else {
				err = exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
					"name="+a.Name, "source=static", "address="+a.Addresses[0], "validate=no").Run()
				for i := 1; i < len(a.Addresses); i++ {
					_ = exec.Command("netsh", "interface", "ipv4", "add", "dnsservers",
						"name="+a.Name, "address="+a.Addresses[i], fmt.Sprintf("index=%d", i+1), "validate=no").Run()
				}
			}
			if err != nil {
				errs = append(errs, a.Name+": "+err.Error())
				continue
			}
			log.Info().Str("adapter", a.Name).Bool("dhcp", a.DHCP).Strs("dns", a.Addresses).Msg("restored adapter DNS")
		}
		_ = exec.Command("ipconfig", "/flushdns").Run()
		if len(errs) > 0 {
			return fmt.Errorf("restore adapter DNS: %s", strings.Join(errs, "; "))
		}
		return nil
	case "resolvectl":
		err := exec.Command("resolvectl", "revert", b.Iface).Run()
		_ = exec.Command("resolvectl", "flush-caches").Run()
		return err
	case "resolvconf":
		return os.WriteFile("/etc/resolv.conf", []byte(b.ResolvConf), 0o644)
	}
	return nil
}

// rewriteResolvConf puts the whitelist resolver first and comments out every
// other nameserver. Prepending alone left the original servers listed, and a
// resolver that failed over to them bypassed the whitelist.
func rewriteResolvConf(original string) string {
	var sb strings.Builder
	sb.WriteString("nameserver 127.0.0.1\n")
	for _, line := range strings.Split(strings.TrimRight(original, "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
			sb.WriteString("# endpoint whitelist: " + line + "\n")
			continue
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// upstreamFromBackup is the resolver the whitelist proxy should forward to,
// taken from the persisted backup. After a crash the live settings say
// 127.0.0.1, so reading them would forward the proxy to itself.
func upstreamFromBackup(b dnsBackup) string {
	for _, a := range b.Adapters {
		for _, addr := range a.Addresses {
			if !strings.HasPrefix(addr, "127.") {
				return addr + ":53"
			}
		}
	}
	for _, line := range strings.Split(b.ResolvConf, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" && !strings.HasPrefix(fields[1], "127.") {
			return fields[1] + ":53"
		}
	}
	return ""
}

func getConnectedWindowsAdapters() ([]string, error) {
	out, err := exec.Command("netsh", "interface", "ipv4", "show", "interfaces").Output()
	if err != nil {
		return nil, err
	}

	var adapters []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "connected") && !strings.Contains(line, "Loopback") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				// The adapter name is the trailing field(s)
				idx := strings.Index(line, "connected")
				name := strings.TrimSpace(line[idx+len("connected"):])
				if name != "" {
					adapters = append(adapters, name)
				}
			}
		}
	}
	return adapters, nil
}

func getWindowsDNSBackup(adapter string) adapterBackup {
	bk := adapterBackup{Name: adapter, DHCP: true}
	out, err := exec.Command("netsh", "interface", "ipv4", "show", "dnsservers", "name="+adapter).Output()
	if err != nil {
		return bk
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	isStatic := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "Statically Configured DNS Servers:") {
			isStatic = true
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				ip := strings.TrimSpace(parts[1])
				if parsed := net.ParseIP(ip); parsed != nil && ip != "None" && ip != "127.0.0.1" {
					bk.Addresses = append(bk.Addresses, ip)
				}
			}
		} else if isStatic {
			ip := line
			if parsed := net.ParseIP(ip); parsed != nil && ip != "127.0.0.1" {
				bk.Addresses = append(bk.Addresses, ip)
			} else if strings.Contains(line, "Register with which suffix:") {
				isStatic = false
			}
		} else if strings.Contains(line, "DNS servers configured through DHCP:") {
			bk.DHCP = true
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				ip := strings.TrimSpace(parts[1])
				if parsed := net.ParseIP(ip); parsed != nil && ip != "None" && ip != "127.0.0.1" {
					bk.Addresses = append(bk.Addresses, ip)
				}
			}
		}
	}

	if len(bk.Addresses) > 0 && isStatic {
		bk.DHCP = false
	}
	return bk
}

func getActiveLinuxInterface() string {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	// "default via 192.168.1.1 dev eth0 ..."
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// GetPrimaryUpstreamDNS discovers the local network's existing DNS server (gateway/DHCP DNS),
// falling back to 1.1.1.1 if not determinable.
func GetPrimaryUpstreamDNS() string {
	switch runtime.GOOS {
	case "windows":
		adapters, err := getConnectedWindowsAdapters()
		if err == nil {
			for _, a := range adapters {
				bk := getWindowsDNSBackup(a)
				for _, addr := range bk.Addresses {
					if addr != "127.0.0.1" && !strings.HasPrefix(addr, "127.") {
						return addr + ":53"
					}
				}
			}
		}
	case "linux":
		data, err := os.ReadFile("/etc/resolv.conf")
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(data))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "nameserver ") {
					parts := strings.Fields(line)
					if len(parts) >= 2 && parts[1] != "127.0.0.1" {
						return parts[1] + ":53"
					}
				}
			}
		}
	}
	return defaultUpstream
}
