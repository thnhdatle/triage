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

// Mapping is the per-request bijection between finding values and
// placeholders used in mask mode. It lives in memory for the request only and
// must never be logged or recorded.
//
// A Mapping is not safe for concurrent use: it is owned by the request's
// dispatch, which calls MaskRequest, Rescan and the unmask methods from one
// goroutine at a time (or serialises them). Rescan mutates the mapping.
type Mapping struct {
	// reserved holds placeholder-grammar strings that occur in the original
	// request or in rescanned text; they are never allocated.
	reserved map[string]struct{}
	// values maps a value to its placeholder; keys is the inverse.
	values map[string]string
	keys   map[string]string
	// next is the last index allocated per type token.
	next map[string]int
	// tokens is the set of type tokens with at least one value.
	tokens map[string]struct{}

	// Derived by rebuild.
	textReplacer *strings.Replacer
	argReplacer  *strings.Replacer
	argForms     map[byte][]form
	prefixes     map[string]struct{}
	maxKeyLen    int
	tokenBytes   [utf8.RuneSelf]bool

	// partial is the partial-unmask pattern of tokens, compiled on first use
	// by partialRe: most responses hold no '[' and never need it, and
	// compiling it per request dominated the allocations of the masked path.
	// partialMu guards the lazy compile in case unmask
	// calls of one mapping ever overlap.
	partialMu    sync.Mutex
	partial      *regexp.Regexp
	partialStale bool
}

// form is one byte sequence that is replaced by a placeholder when masking.
type form struct {
	old         string
	placeholder string
}

// NewMapping builds the mapping of a request from its scan findings. Values are
// numbered per type in finding order; a value seen again (under any type) keeps
// its first placeholder. Findings without a value (ner_unavailable) are
// ignored. An index whose placeholder already occurs anywhere in req (text
// parts and tool-call arguments of every message, system included) is skipped.
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

// Empty reports whether the mapping has no values, so masking is the identity.
func (m *Mapping) Empty() bool { return len(m.values) == 0 }

// Rescan scans continuation text (assistant text produced so far, to be sent
// to a cloud target) with the same detectors, gives new values new
// placeholders (skipping indices already used or occurring in the original
// request or in text) and returns text with known and new values masked. On a
// detector error, or when a detector reports ner_unavailable
// (ErrRescanIncomplete), the mapping is left unchanged and the text must not
// be sent to a cloud target.
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

// Leaks reports whether any mapped value occurs byte-for-byte in a text part
// of req, or in a tool-call argument as is or JSON-escaped. Dispatch can use it
// as a fail-closed check on a masked request: MaskRequest guarantees no leak
// for values that contain a byte outside [A-Z0-9_] and no '[' or ']', but a
// pathological value (e.g. "EMAIL" or "1]]") can reappear inside or around a
// placeholder.
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

// reserve records every placeholder-grammar string occurring in texts.
func (m *Mapping) reserve(texts []string) {
	for _, t := range texts {
		for _, p := range reservedPattern.FindAllString(t, -1) {
			m.reserved[p] = struct{}{}
		}
	}
}

// add allocates placeholders for new values in finding order.
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

// rebuild recomputes the replacers and lookup tables from values and keys.
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
	// Longest form first; on equal length the raw value (added first) wins.
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

// partialRe returns the partial-unmask pattern of the current tokens (nil
// when there are none), compiling it on the first call after a rebuild.
func (m *Mapping) partialRe() *regexp.Regexp {
	m.partialMu.Lock()
	defer m.partialMu.Unlock()
	if m.partialStale {
		m.partial, m.partialStale = partialPattern(m.tokens), false
	}
	return m.partial
}

// sortLongestFirst orders values so that, at one position, the longest value
// wins; equal lengths are ordered bytewise for determinism.
func sortLongestFirst(vals []string) {
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i]) != len(vals[j]) {
			return len(vals[i]) > len(vals[j])
		}
		return vals[i] < vals[j]
	})
}
