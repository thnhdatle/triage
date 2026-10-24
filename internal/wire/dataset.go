// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

const maxDatasetLine = 16 << 20

type datasetItem struct {
	ID            string        `json:"id"`
	Prompt        string        `json:"prompt"`
	Messages      []wireMessage `json:"messages"`
	Reference     string        `json:"reference"`
	Scorer        string        `json:"scorer"`
	WeakCorrect   *bool         `json:"weak_correct"`
	StrongCorrect *bool         `json:"strong_correct"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wirePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (d datasetItem) request(model string) (chat.Request, error) {
	req := chat.Request{ID: d.ID, Model: model}
	if len(d.Messages) == 0 {
		if d.Prompt == "" {
			return req, errors.New("item has neither messages nor prompt")
		}
		req.Messages = []chat.Message{chat.TextMessage(chat.RoleUser, d.Prompt)}
		return req, nil
	}
	for _, m := range d.Messages {
		text, err := contentText(m.Content)
		if err != nil {
			return req, err
		}
		req.Messages = append(req.Messages, chat.TextMessage(chat.Role(m.Role), text))
	}
	return req, nil
}

func contentText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("message content: %w", err)
		}
		return s, nil
	}
	var parts []wirePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content: %w", err)
	}
	var b bytes.Buffer
	for _, p := range parts {
		if p.Type != "text" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

func readDataset(path string) ([]datasetItem, error) {
	f, err := os.Open(path) //nolint:gosec // operator-chosen dataset path
	if err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxDatasetLine)
	var items []datasetItem
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var it datasetItem
		if err := json.Unmarshal(b, &it); err != nil {
			return nil, fmt.Errorf("dataset %s line %d: %w", path, line, err)
		}
		if it.ID == "" {
			it.ID = strconv.Itoa(line)
		}
		if _, err := it.request(""); err != nil {
			return nil, fmt.Errorf("dataset %s line %d: %w", path, line, err)
		}
		items = append(items, it)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dataset %s: %w", path, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("dataset %s: no items", path)
	}
	return items, nil
}
