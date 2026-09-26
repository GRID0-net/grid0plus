package nex

import (
	"fmt"
	"strconv"
	"strings"
)

// StationURL is where a client or server can be reached, plus the NAT facts
// Pia needs to reach it: "prudps:/address=1.2.3.4;port=1;PID=2;sid=1".
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NEX-Common-Types#stationurl
//
// Parameters keep the order they were parsed or first set in. The order means
// nothing to NEX, but a station this server hands back is usually one a
// console registered, and re-emitting it byte for byte where nothing changed
// keeps a capture of the two directly comparable.
type StationURL struct {
	Scheme string
	keys   []string
	vals   map[string]string
}

// NewStationURL returns an empty station with the given scheme.
func NewStationURL(scheme string) *StationURL {
	return &StationURL{Scheme: scheme, vals: map[string]string{}}
}

// ParseStationURL parses the string form. The empty string is an empty
// "prudp" station, which is how NEX writes "no station".
func ParseStationURL(s string) (*StationURL, error) {
	if s == "" {
		return NewStationURL("prudp"), nil
	}
	scheme, rest, ok := strings.Cut(s, ":/")
	if !ok {
		return nil, fmt.Errorf("nex: station url %q has no scheme", s)
	}
	u := NewStationURL(scheme)
	if rest == "" {
		return u, nil
	}
	for _, field := range strings.Split(rest, ";") {
		if field == "" {
			continue
		}
		k, v, ok := strings.Cut(field, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("nex: station url %q has a malformed field %q", s, field)
		}
		u.Set(k, v)
	}
	return u, nil
}

// String is the wire form.
func (u *StationURL) String() string {
	var b strings.Builder
	b.WriteString(u.Scheme)
	b.WriteString(":/")
	for i, k := range u.keys {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(u.vals[k])
	}
	return b.String()
}

// Get returns a parameter, or "" if it is absent.
func (u *StationURL) Get(key string) string { return u.vals[key] }

// Has reports whether a parameter is present.
func (u *StationURL) Has(key string) bool {
	_, ok := u.vals[key]
	return ok
}

// Int returns a numeric parameter, or 0 if it is absent or not a number.
func (u *StationURL) Int(key string) int64 {
	n, _ := strconv.ParseInt(u.vals[key], 10, 64)
	return n
}

// Uint returns an unsigned numeric parameter (PIDs are u64 on Switch).
func (u *StationURL) Uint(key string) uint64 {
	n, _ := strconv.ParseUint(u.vals[key], 10, 64)
	return n
}

// Set sets a parameter, keeping its position if it already exists.
func (u *StationURL) Set(key, value string) {
	if _, ok := u.vals[key]; !ok {
		u.keys = append(u.keys, key)
	}
	u.vals[key] = value
}

// SetInt sets a numeric parameter.
func (u *StationURL) SetInt(key string, value int64) { u.Set(key, strconv.FormatInt(value, 10)) }

// SetUint sets an unsigned numeric parameter.
func (u *StationURL) SetUint(key string, value uint64) { u.Set(key, strconv.FormatUint(value, 10)) }

// Delete removes a parameter.
func (u *StationURL) Delete(key string) {
	if _, ok := u.vals[key]; !ok {
		return
	}
	delete(u.vals, key)
	for i, k := range u.keys {
		if k == key {
			u.keys = append(u.keys[:i:i], u.keys[i+1:]...)
			break
		}
	}
}

// Copy returns an independent copy.
func (u *StationURL) Copy() *StationURL {
	c := NewStationURL(u.Scheme)
	for _, k := range u.keys {
		c.Set(k, u.vals[k])
	}
	return c
}

// Station type flags ('type' parameter).
const (
	StationBehindNAT = 1
	StationPublic    = 2
)
