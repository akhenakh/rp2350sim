package main

import (
	"fmt"
	"strconv"
	"strings"
)

func parseOptionalHex32(s, what string) (uint32, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseHex32(s, what)
}

func parseHex32(s, what string) (uint32, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "0x"), "0X")
	if raw == "" {
		return 0, fmt.Errorf("%s expects a hexadecimal 32-bit value", what)
	}
	v, err := strconv.ParseUint(raw, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	return uint32(v), nil
}

type pokeWrite struct {
	addr uint32
	val  uint32
}

func parsePokes(s string) ([]pokeWrite, error) {
	var out []pokeWrite
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		addr, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("-poke %q expects addr=val", entry)
		}
		a, err := parseHex32(addr, "-poke address")
		if err != nil {
			return nil, err
		}
		v, err := parseHex32(value, "-poke value")
		if err != nil {
			return nil, err
		}
		out = append(out, pokeWrite{addr: a, val: v})
	}
	return out, nil
}
