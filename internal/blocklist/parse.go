package blocklist

import (
	"bufio"
	"hash/maphash"
	"io"
	"net/netip"
	"slices"
	"strings"
)

var seed = maphash.MakeSeed()

func hashName(s string) uint64 {
	return maphash.String(seed, s)
}

type Index struct {
	names      []uint64
	exceptions []uint64
}

func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.names)
}

func (ix *Index) Match(name string) bool {
	if ix == nil || len(ix.names) == 0 {
		return false
	}
	return walk(ix.names, name) && !walk(ix.exceptions, name)
}

func walk(set []uint64, name string) bool {
	if len(set) == 0 {
		return false
	}
	for {
		if _, ok := slices.BinarySearch(set, hashName(name)); ok {
			return true
		}
		_, rest, found := strings.Cut(name, ".")
		if !found {
			return false
		}
		name = rest
	}
}

var cosmetic = []string{"##", "#@#", "#?#", "#$#"}

var boilerplate = map[string]bool{
	"localhost":             true,
	"localhost.localdomain": true,
	"local":                 true,
	"broadcasthost":         true,
	"0.0.0.0":               true,
}

func Parse(r io.Reader) (*Index, int, error) {
	var names, exceptions []uint64
	skipped := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '!' || line[0] == '[' {
			skipped++
			continue
		}
		if slices.ContainsFunc(cosmetic, func(c string) bool { return strings.Contains(line, c) }) {
			skipped++
			continue
		}
		if before, _, found := strings.Cut(line, "#"); found {
			line = strings.TrimSpace(before)
			if line == "" {
				skipped++
				continue
			}
		}

		if rest, ok := strings.CutPrefix(line, "@@||"); ok {
			if d, ok := adblockDomain(rest); ok {
				exceptions = append(exceptions, hashName(d))
			} else {
				skipped++
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "||"); ok {
			if d, ok := adblockDomain(rest); ok {
				names = append(names, hashName(d))
			} else {
				skipped++
			}
			continue
		}
		if len(line) > 1 && line[0] == '/' && line[len(line)-1] == '/' {
			skipped++
			continue
		}

		fields := strings.Fields(line)
		if _, err := netip.ParseAddr(fields[0]); err == nil {
			if len(fields) == 1 {
				skipped++
				continue
			}
			for _, f := range fields[1:] {
				if d, ok := listDomain(f); ok {
					names = append(names, hashName(d))
				} else {
					skipped++
				}
			}
			continue
		}
		if len(fields) != 1 {
			skipped++
			continue
		}
		if d, ok := listDomain(fields[0]); ok {
			names = append(names, hashName(d))
		} else {
			skipped++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	slices.Sort(names)
	slices.Sort(exceptions)
	return &Index{names: slices.Clip(slices.Compact(names)), exceptions: slices.Clip(slices.Compact(exceptions))}, skipped, nil
}

func adblockDomain(rule string) (string, bool) {
	if strings.ContainsAny(rule, "$/") {
		return "", false
	}
	rule = strings.TrimSuffix(strings.TrimSuffix(rule, "|"), "^")
	return listDomain(rule)
}

func listDomain(s string) (string, bool) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimPrefix(s, "*.")), ".")
	if boilerplate[s] || strings.HasPrefix(s, "ip6-") {
		return "", false
	}
	if len(s) == 0 || len(s) > 253 || !strings.Contains(s, ".") {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := range len(label) {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", false
			}
		}
	}
	return s, true
}
