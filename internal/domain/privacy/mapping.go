// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Mapping holds the placeholders of one request.
type Mapping struct {
	reserved map[string]struct{}
	values   map[string]string
	keys     map[string]string
	next     map[string]int
	tokens   map[string]struct{}

	textReplacer *strings.Replacer
	argReplacer  *strings.Replacer
	argForms     map[byte][]form
	prefixes     map[string]struct{}
	maxKeyLen    int
	tokenBytes   [utf8.RuneSelf]bool

	partialMu    sync.Mutex
	partial      *regexp.Regexp
	partialStale bool
}

type form struct {
	old         string
	placeholder string
}

// NewMapping builds the mapping for the findings of req.
func NewMapping(req chat.Request, findings []Finding) *Mapping {
	m := &Mapping{
		reserved: map[string]struct{}{},
		values:   map[string]string{},
		keys:     map[string]string{},
		next:     map[string]int{},
		tokens:   map[string]struct{}{},
	}
	m.reserve(allTexts(req))
	m.add(findings)
	m.rebuild()
	return m
}

func (m *Mapping) Empty() bool { return len(m.values) == 0 }

func (m *Mapping) Rescan(ctx context.Context, detectors []Detector, text string) (string, error) {
	var found []Finding
	for _, d := range detectors {
		fs, err := detect(ctx, d, text)
		if err != nil {
			return "", err
		}
		for _, f := range fs {
			if f.Type == TypeNERUnavailable {
				return "", fmt.Errorf("%w (detector %s)", ErrRescanIncomplete, d.ID())
			}
		}
		found = append(found, fs...)
	}
	m.reserve([]string{text})
	m.add(found)
	m.rebuild()
	return m.maskText(text), nil
}

func (m *Mapping) Leaks(req chat.Request) bool {
	for _, msg := range req.Messages {
		for _, p := range msg.Content {
			if p.Type == chat.PartText && m.containsValue(p.Text, false) {
				return true
			}
		}
		for _, tc := range msg.ToolCalls {
			if m.containsValue(tc.Function.Arguments, true) {
				return true
			}
		}
	}
	return false
}

func (m *Mapping) containsValue(s string, escaped bool) bool {
	for v := range m.values {
		if strings.Contains(s, v) || (escaped && strings.Contains(s, jsonEscape(v))) {
			return true
		}
	}
	return false
}

func (m *Mapping) reserve(texts []string) {
	for _, t := range texts {
		for _, p := range reservedPattern.FindAllString(t, -1) {
			m.reserved[p] = struct{}{}
		}
	}
}

func (m *Mapping) add(findings []Finding) {
	for _, f := range findings {
		if f.Value == "" || f.Type == TypeNERUnavailable {
			continue
		}
		if _, ok := m.values[f.Value]; ok {
			continue
		}
		token := typeToken(f.Type)
		n := m.next[token]
		var p string
		for {
			n++
			p = placeholderFor(token, n)
			_, isReserved := m.reserved[p]
			_, isUsed := m.keys[p]
			if !isReserved && !isUsed {
				break
			}
		}
		m.next[token] = n
		m.values[f.Value] = p
		m.keys[p] = f.Value
		m.tokens[token] = struct{}{}
	}
}

func (m *Mapping) rebuild() {
	vals := make([]string, 0, len(m.values))
	for v := range m.values {
		vals = append(vals, v)
	}
	sortLongestFirst(vals)
	pairs := make([]string, 0, 2*len(vals))
	forms := make([]form, 0, 2*len(vals))
	for _, v := range vals {
		pairs = append(pairs, v, m.values[v])
		forms = append(forms, form{old: v, placeholder: m.values[v]})
		if e := jsonEscape(v); e != v {
			forms = append(forms, form{old: e, placeholder: m.values[v]})
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i].old) > len(forms[j].old) })
	argPairs := make([]string, 0, 2*len(forms))
	m.argForms = map[byte][]form{}
	seen := map[string]bool{}
	for _, f := range forms {
		if seen[f.old] {
			continue
		}
		seen[f.old] = true
		argPairs = append(argPairs, f.old, f.placeholder)
		m.argForms[f.old[0]] = append(m.argForms[f.old[0]], f)
	}
	m.textReplacer, m.argReplacer = nil, nil
	if len(pairs) > 0 {
		m.textReplacer = strings.NewReplacer(pairs...)
		m.argReplacer = strings.NewReplacer(argPairs...)
	}
	m.prefixes = map[string]struct{}{}
	m.maxKeyLen = 0
	for k := range m.keys {
		m.maxKeyLen = max(m.maxKeyLen, len(k))
		for i := 1; i < len(k); i++ {
			m.prefixes[k[:i]] = struct{}{}
		}
	}
	m.partialMu.Lock()
	m.partial, m.partialStale = nil, true
	m.partialMu.Unlock()
	m.tokenBytes = tokenByteSet(m.tokens)
}

func (m *Mapping) partialRe() *regexp.Regexp {
	m.partialMu.Lock()
	defer m.partialMu.Unlock()
	if m.partialStale {
		m.partial, m.partialStale = partialPattern(m.tokens), false
	}
	return m.partial
}

func sortLongestFirst(vals []string) {
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i]) != len(vals[j]) {
			return len(vals[i]) > len(vals[j])
		}
		return vals[i] < vals[j]
	})
}
