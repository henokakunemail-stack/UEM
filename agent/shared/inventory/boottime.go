package inventory

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseProcStatBtime returns the boot time from Linux /proc/stat ("btime
// <unix seconds>") as RFC 3339 UTC, or "" when the line is absent.
func ParseProcStatBtime(stat string) string {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "btime" {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && n > 0 {
				return time.Unix(n, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return ""
}

var macBoottimeRe = regexp.MustCompile(`sec\s*=\s*(\d+)`)

// ParseMacBoottime returns the boot time from `sysctl -n kern.boottime`
// ("{ sec = 1700000000, usec = 12 } Tue Nov ...") as RFC 3339 UTC, or "".
func ParseMacBoottime(out string) string {
	m := macBoottimeRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.Unix(n, 0).UTC().Format(time.RFC3339)
}
