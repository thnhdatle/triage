// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(marshal(v))
}

func writeError(w http.ResponseWriter, status int, typ, code, msg string) {
	var codeVal any
	if code != "" {
		codeVal = code
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"message": msg, "type": typ, "param": nil, "code": codeVal,
	}})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "invalid_request_error", "", msg)
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		badRequest(w, "cannot read request body: "+err.Error())
		return nil, false
	}
	return body, true
}

func quoteBytes(s string) string {
	const hexDigits = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func splitChunks(s string, size int, splitRunes bool) []string {
	if size < 1 {
		size = defaultChunkBytes
	}
	var out []string
	for i := 0; i < len(s); {
		end := min(i+size, len(s))
		if !splitRunes {
			for end < len(s) && !utf8.RuneStart(s[end]) {
				end++
			}
		}
		out = append(out, s[i:end])
		i = end
	}
	return out
}

func tokenCount(s string) int { return len(strings.Fields(s)) }

func waitErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fakellm: wait: %w", err)
	}
	return nil
}

func (s *Server) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return waitErr(ctx)
	}
	select {
	case <-ctx.Done():
		return waitErr(ctx)
	case <-s.clock.After(d):
		return nil
	}
}
