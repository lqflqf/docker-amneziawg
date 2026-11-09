// Package conf models WireGuard/AmneziaWG conf files: one writer for every
// file this program makes, and a parser for existing confs (legacy import,
// the active interface section).
package conf

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

// KV is one "Key = Value" line.
type KV struct{ Key, Value string }

// Section is "[Name]", optional comment lines, then key-value lines.
type Section struct {
	Name     string
	Comments []string // written as "# <comment>" after the header
	Lines    []KV
}

// File is a list of sections.
type File struct{ Sections []Section }

// Add appends a line; empty values are never written.
func (s *Section) Add(key, value string) {
	if value == "" {
		return
	}
	s.Lines = append(s.Lines, KV{key, value})
}

// Get returns the first value of key (case-insensitive).
func (s *Section) Get(key string) string {
	for _, l := range s.Lines {
		if strings.EqualFold(l.Key, key) {
			return l.Value
		}
	}
	return ""
}

// All returns every value of key.
func (s *Section) All(key string) []string {
	var out []string
	for _, l := range s.Lines {
		if strings.EqualFold(l.Key, key) {
			out = append(out, l.Value)
		}
	}
	return out
}

// Check refuses values that would break the file: newlines and empty keys.
func (f *File) Check() error {
	for _, s := range f.Sections {
		for _, c := range s.Comments {
			if strings.ContainsAny(c, "\r\n") {
				return fmt.Errorf("[%s]: a comment contains a newline", s.Name)
			}
		}
		for _, l := range s.Lines {
			if l.Key == "" || strings.ContainsAny(l.Key+l.Value, "\r\n") {
				return fmt.Errorf("[%s] %s: a key or value contains a newline", s.Name, l.Key)
			}
		}
	}
	return nil
}

// String writes the file: a blank line between sections, a final newline.
func (f *File) String() string {
	var b strings.Builder
	for i, s := range f.Sections {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", s.Name)
		for _, c := range s.Comments {
			fmt.Fprintf(&b, "# %s\n", c)
		}
		for _, l := range s.Lines {
			fmt.Fprintf(&b, "%s = %s\n", l.Key, l.Value)
		}
	}
	return b.String()
}

// Interface returns the first [Interface] section, or nil.
func (f *File) Interface() *Section {
	for i := range f.Sections {
		if strings.EqualFold(f.Sections[i].Name, "Interface") {
			return &f.Sections[i]
		}
	}
	return nil
}

// Peers returns the [Peer] sections.
func (f *File) Peers() []*Section {
	var out []*Section
	for i := range f.Sections {
		if strings.EqualFold(f.Sections[i].Name, "Peer") {
			out = append(out, &f.Sections[i])
		}
	}
	return out
}

// Parse reads a conf. Keys are split on the first "=" only (I1-I5 values
// contain "="). Lines before the first section are an error.
func Parse(text string) (*File, error) {
	f := &File{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if len(f.Sections) > 0 {
				s := &f.Sections[len(f.Sections)-1]
				s.Comments = append(s.Comments, strings.TrimSpace(strings.TrimPrefix(line, "#")))
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			f.Sections = append(f.Sections, Section{Name: strings.TrimSpace(line[1 : len(line)-1])})
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: no '=' in %q", n, line)
		}
		if len(f.Sections) == 0 {
			return nil, fmt.Errorf("line %d: %q is outside a section", n, line)
		}
		s := &f.Sections[len(f.Sections)-1]
		s.Lines = append(s.Lines, KV{strings.TrimSpace(k), strings.TrimSpace(v)})
	}
	return f, sc.Err()
}

// Diff compares two confs key by key. Order, whitespace and comments are
// ignored; peers are matched by PublicKey. normalize may rewrite a value
// before the comparison (for example "10.0.0.2" vs "10.0.0.2/32"). It returns
// the differing "Section.Key" names, sorted.
func Diff(a, b *File, normalize func(key, value string) string) []string {
	if normalize == nil {
		normalize = func(_, v string) string { return v }
	}
	type bag map[string][]string
	collect := func(s *Section) bag {
		m := bag{}
		for _, l := range s.Lines {
			k := strings.ToLower(l.Key)
			m[k] = append(m[k], normalize(k, l.Value))
		}
		for k := range m {
			sort.Strings(m[k])
		}
		return m
	}
	diffBags := func(prefix string, x, y bag, out map[string]bool) {
		for k, xv := range x {
			if strings.Join(xv, "\x00") != strings.Join(y[k], "\x00") {
				out[prefix+k] = true
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				out[prefix+k] = true
			}
		}
	}
	out := map[string]bool{}
	ai, bi := a.Interface(), b.Interface()
	switch {
	case ai == nil && bi == nil:
	case ai == nil || bi == nil:
		out["Interface"] = true
	default:
		diffBags("Interface.", collect(ai), collect(bi), out)
	}
	pa, pb := map[string]*Section{}, map[string]*Section{}
	for _, p := range a.Peers() {
		pa[p.Get("PublicKey")] = p
	}
	for _, p := range b.Peers() {
		pb[p.Get("PublicKey")] = p
	}
	for k, p := range pa {
		q, ok := pb[k]
		if !ok {
			out["Peer.PublicKey"] = true
			continue
		}
		diffBags("Peer.", collect(p), collect(q), out)
	}
	for k := range pb {
		if _, ok := pa[k]; !ok {
			out["Peer.PublicKey"] = true
		}
	}
	var keys []string
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
