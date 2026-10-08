package update

import (
	"errors"
	"strconv"
	"strings"
)

// belowMinimum reports whether version is older than floor. It is the
// downgrade guard for a release's minimum_supported_version, and it is the
// only place in the update path that interprets a version string as a
// number rather than an opaque token.
//
// Only the numeric major.minor.patch segments are compared. A prerelease tag
// ("1.2.3-rc1") is stripped, not ordered against another prerelease: a floor
// is a coarse fleet-wide statement, and treating rc ordering as significant
// here would buy nothing but a way to get the comparison wrong. Build
// metadata is ignored for the same reason.
//
// Malformed input fails open in the direction that does not install: a floor
// the agent cannot parse is refused, and a device version it cannot parse is
// treated as too old to judge. An operator who has shipped an unparseable
// floor gets a failed task with the reason in it, not a silent install.
func belowMinimum(version, floor string) bool {
	v, err := parseVersion(version)
	if err != nil {
		return true
	}
	f, err := parseVersion(floor)
	if err != nil {
		return true
	}
	return v[0] < f[0] || (v[0] == f[0] && v[1] < f[1]) ||
		(v[0] == f[0] && v[1] == f[1] && v[2] < f[2])
}

// parseVersion returns the three numeric segments of a semver-ish string.
// One- or two-part versions are accepted and padded with zeros ("1.2" means
// 1.2.0) because agent builds have been stamped as two-part version strings
// in the field, and a floor that could not see them would refuse every
// release in the fleet.
func parseVersion(s string) ([3]int, error) {
	var out [3]int
	s = strings.TrimSpace(s)
	if s == "" {
		return out, errors.New("empty version")
	}
	// Drop prerelease and build metadata.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ".", 4)
	if len(parts) > 3 {
		return out, errors.New("version has more than three segments")
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, errors.New("version segment is not a non-negative integer: " + p)
		}
		out[i] = n
	}
	return out, nil
}
