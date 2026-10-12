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
	"sync"

	"github.com/rs/zerolog/log"
)

var (
	adapterMu     sync.Mutex
	activeRestore func()
)

// ConfigureLoopbackDNS points the system's active network adapter(s) to 127.0.0.1,
// returning a restore function that resets the adapters to their previous state.
func ConfigureLoopbackDNS() (func(), error) {
	adapterMu.Lock()
	defer adapterMu.Unlock()

	// If a previous configuration was already active, don't nest restores; keep original
	if activeRestore != nil {
		return activeRestore, nil
	}

	var restore func()
	var err error

	switch runtime.GOOS {
	case "windows":
		restore, err = configureWindowsDNS()
	case "linux":
		restore, err = configureLinuxDNS()
	default:
		return func() {}, nil
	}

	if err != nil {
		return nil, err
	}

	activeRestore = restore
	return func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		if activeRestore != nil {
			activeRestore()
			activeRestore = nil
		}
	}, nil
}

// RestoreOriginalDNS immediately restores the system DNS adapters if modified.
func RestoreOriginalDNS() {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	if activeRestore != nil {
		activeRestore()
		activeRestore = nil
	}
}

type windowsAdapterBackup struct {
	name      string
	isDHCP    bool
	addresses []string
}

func configureWindowsDNS() (func(), error) {
	adapters, err := getConnectedWindowsAdapters()
	if err != nil || len(adapters) == 0 {
		return nil, fmt.Errorf("no connected network adapter found: %w", err)
	}

	var backups []windowsAdapterBackup
	for _, adapter := range adapters {
		bk := getWindowsDNSBackup(adapter)
		backups = append(backups, bk)

		// Set DNS to 127.0.0.1
		cmd := exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
			"name="+adapter, "source=static", "address=127.0.0.1", "validate=no")
		if err := cmd.Run(); err != nil {
			log.Warn().Str("adapter", adapter).Err(err).Msg("failed to set 127.0.0.1 DNS on adapter")
		} else {
			log.Info().Str("adapter", adapter).Msg("configured adapter DNS to 127.0.0.1")
		}
	}

	restore := func() {
		for _, bk := range backups {
			if bk.isDHCP || len(bk.addresses) == 0 {
				_ = exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
					"name="+bk.name, "source=dhcp").Run()
				log.Info().Str("adapter", bk.name).Msg("restored adapter DNS to DHCP")
			} else {
				// Restore primary static DNS
				_ = exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
					"name="+bk.name, "source=static", "address="+bk.addresses[0], "validate=no").Run()
				// Add secondary static DNS if present
				for i := 1; i < len(bk.addresses); i++ {
					_ = exec.Command("netsh", "interface", "ipv4", "add", "dnsservers",
						"name="+bk.name, "address="+bk.addresses[i], fmt.Sprintf("index=%d", i+1), "validate=no").Run()
				}
				log.Info().Str("adapter", bk.name).Strs("dns", bk.addresses).Msg("restored adapter static DNS")
			}
		}
		_ = exec.Command("ipconfig", "/flushdns").Run()
	}

	_ = exec.Command("ipconfig", "/flushdns").Run()
	return restore, nil
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

func getWindowsDNSBackup(adapter string) windowsAdapterBackup {
	bk := windowsAdapterBackup{name: adapter, isDHCP: true}
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
					bk.addresses = append(bk.addresses, ip)
				}
			}
		} else if isStatic {
			ip := line
			if parsed := net.ParseIP(ip); parsed != nil && ip != "127.0.0.1" {
				bk.addresses = append(bk.addresses, ip)
			} else if strings.Contains(line, "Register with which suffix:") {
				isStatic = false
			}
		} else if strings.Contains(line, "DNS servers configured through DHCP:") {
			bk.isDHCP = true
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				ip := strings.TrimSpace(parts[1])
				if parsed := net.ParseIP(ip); parsed != nil && ip != "None" && ip != "127.0.0.1" {
					bk.addresses = append(bk.addresses, ip)
				}
			}
		}
	}

	if len(bk.addresses) > 0 && isStatic {
		bk.isDHCP = false
	}
	return bk
}

func configureLinuxDNS() (func(), error) {
	// Try resolvectl first
	if _, err := exec.LookPath("resolvectl"); err == nil {
		iface := getActiveLinuxInterface()
		if iface != "" {
			if err := exec.Command("resolvectl", "dns", iface, "127.0.0.1").Run(); err == nil {
				log.Info().Str("interface", iface).Msg("configured Linux DNS via resolvectl")
				return func() {
					_ = exec.Command("resolvectl", "revert", iface).Run()
					_ = exec.Command("resolvectl", "flush-caches").Run()
					log.Info().Str("interface", iface).Msg("reverted Linux DNS via resolvectl")
				}, nil
			}
		}
	}

	// Fallback to /etc/resolv.conf
	resolvPath := "/etc/resolv.conf"
	original, err := os.ReadFile(resolvPath)
	if err != nil {
		return nil, fmt.Errorf("read /etc/resolv.conf: %w", err)
	}

	newContent := "nameserver 127.0.0.1\n" + string(original)
	if err := os.WriteFile(resolvPath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("write /etc/resolv.conf: %w", err)
	}
	log.Info().Msg("configured Linux DNS via /etc/resolv.conf")

	return func() {
		_ = os.WriteFile(resolvPath, original, 0644)
		log.Info().Msg("restored /etc/resolv.conf")
	}, nil
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
	if runtime.GOOS == "windows" {
		adapters, err := getConnectedWindowsAdapters()
		if err == nil {
			for _, a := range adapters {
				bk := getWindowsDNSBackup(a)
				for _, addr := range bk.addresses {
					if addr != "127.0.0.1" && !strings.HasPrefix(addr, "127.") {
						return addr + ":53"
					}
				}
			}
		}
	} else if runtime.GOOS == "linux" {
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

// Ensure unused error import doesn't cause build error if not referenced
var _ = errors.New
