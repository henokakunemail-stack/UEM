package x11

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
)

// Auth is the credential sent in the connection setup.
type Auth struct {
	Name string
	Data []byte
}

const (
	familyLocal = 256
	familyWild  = 65535
	cookieName  = "MIT-MAGIC-COOKIE-1"
)

type xauthEntry struct {
	family  uint16
	address string
	number  string
	name    string
	data    []byte
}

// ReadXauthority finds the cookie for a local display in an Xauthority file:
// an entry for this display number (or any number), for this host (or any
// host), using MIT-MAGIC-COOKIE-1. An entry naming this host is preferred over
// a wildcard one. A missing file or entry returns nil and no error: many
// servers (Xvfb without -auth among them) need no cookie at all.
func ReadXauthority(path string, display int, hostname string) (*Auth, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := parseXauthority(f)
	if err != nil {
		return nil, err
	}
	num := strconv.Itoa(display)
	var wild *Auth
	for _, e := range entries {
		if e.name != cookieName || (e.number != num && e.number != "") {
			continue
		}
		switch {
		case e.family == familyLocal && e.address == hostname:
			return &Auth{Name: e.name, Data: e.data}, nil
		case e.family == familyWild, e.family == familyLocal:
			if wild == nil {
				wild = &Auth{Name: e.name, Data: e.data}
			}
		}
	}
	return wild, nil
}

func parseXauthority(r io.Reader) ([]xauthEntry, error) {
	var out []xauthEntry
	readField := func() ([]byte, error) {
		var n uint16
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			return nil, err
		}
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	for {
		var fam uint16
		if err := binary.Read(r, binary.BigEndian, &fam); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		var fields [4][]byte
		for i := range fields {
			b, err := readField()
			if err != nil {
				return nil, errors.New("truncated Xauthority entry")
			}
			fields[i] = b
		}
		out = append(out, xauthEntry{family: fam, address: string(fields[0]),
			number: string(fields[1]), name: string(fields[2]), data: fields[3]})
	}
}
