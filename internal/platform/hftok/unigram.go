// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"
)

// unkPenalty is HF's K_UNK_PENALTY: an unknown character scores
// min_score - 10.
const unkPenalty = 10.0

// unigram ports HF Unigram::encode_optimized (SentencePiece's Viterbi best
// path) with fuse_unk. sugarme's Unigram is not used: it caps pieces at 20
// bytes and adds a 0.1-per-byte length bonus, which changes segmentations
// (e.g. 中华人民共和国 is one XLM-R piece).
type unigram struct {
	ids      map[string]int
	scores   []float64
	unkID    int
	unkScore float64
	maxBytes int
}

type scoredPiece struct {
	piece string
	score float64
}

func (p *scoredPiece) UnmarshalJSON(b []byte) error {
	var pair [2]json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return fmt.Errorf("unigram piece: %w", err)
	}
	if err := json.Unmarshal(pair[0], &p.piece); err != nil {
		return fmt.Errorf("unigram piece: %w", err)
	}
	if err := json.Unmarshal(pair[1], &p.score); err != nil {
		return fmt.Errorf("unigram score: %w", err)
	}
	return nil
}

func newUnigram(raw json.RawMessage) (*unigram, error) {
	var cfg struct {
		UnkID        *int          `json:"unk_id"`
		Vocab        []scoredPiece `json:"vocab"`
		ByteFallback bool          `json:"byte_fallback"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: Unigram: %w", ErrInvalid, err)
	}
	if cfg.ByteFallback {
		return nil, fmt.Errorf("%w: Unigram byte_fallback", ErrUnsupported)
	}
	if cfg.UnkID == nil || *cfg.UnkID < 0 || *cfg.UnkID >= len(cfg.Vocab) {
		return nil, fmt.Errorf("%w: Unigram without valid unk_id", ErrUnsupported)
	}
	u := &unigram{
		ids:    make(map[string]int, len(cfg.Vocab)),
		scores: make([]float64, len(cfg.Vocab)),
		unkID:  *cfg.UnkID,
	}
	minScore := math.Inf(1)
	for id, p := range cfg.Vocab {
		u.ids[p.piece] = id // last duplicate wins, as in HF's token_to_ids
		u.scores[id] = p.score
		minScore = min(minScore, p.score)
		u.maxBytes = max(u.maxBytes, len(p.piece))
	}
	u.unkScore = minScore - unkPenalty
	return u, nil
}

type bestNode struct {
	score float64
	start int
	id    int
	reach bool
}

func (u *unigram) tokenize(word string, dst []int) []int {
	return u.backtrack(word, u.viterbi(word), dst)
}

// viterbi fills best[end] with the best path ending at byte end; pieces are
// tried shortest first and replace a node only on a strictly higher score,
// matching HF's tie-breaking.
func (u *unigram) viterbi(word string) []bestNode {
	n := len(word)
	best := make([]bestNode, n+1)
	relax := func(end, start, id int, score float64) {
		if t := &best[end]; !t.reach || score > t.score {
			*t = bestNode{score: score, start: start, id: id, reach: true}
		}
	}
	for start := 0; start < n; {
		base := best[start].score
		_, charLen := utf8.DecodeRuneInString(word[start:])
		hasSingle := false
		limit := min(n, start+u.maxBytes)
		for end := start + charLen; end <= limit; {
			if id, ok := u.ids[word[start:end]]; ok {
				relax(end, start, id, base+u.scores[id])
				hasSingle = hasSingle || end-start == charLen
			}
			if end == n {
				break
			}
			_, size := utf8.DecodeRuneInString(word[end:])
			end += size
		}
		if !hasSingle {
			relax(start+charLen, start, u.unkID, base+u.unkScore)
		}
		start += charLen
	}
	return best
}

// backtrack appends the best path's ids to dst. Runs of unk nodes are fused
// into one piece and, as in HF, every piece is mapped to its id by string.
func (u *unigram) backtrack(word string, best []bestNode, dst []int) []int {
	first := len(dst)
	runEnd := -1
	for end := len(word); end > 0; end = best[end].start {
		node := best[end]
		if node.id == u.unkID {
			if runEnd < 0 {
				runEnd = end
			}
			continue
		}
		if runEnd >= 0 {
			dst = append(dst, u.lookup(word[end:runEnd]))
			runEnd = -1
		}
		dst = append(dst, u.lookup(word[node.start:end]))
	}
	if runEnd >= 0 {
		dst = append(dst, u.lookup(word[:runEnd]))
	}
	slices.Reverse(dst[first:])
	return dst
}

func (u *unigram) lookup(piece string) int {
	if id, ok := u.ids[piece]; ok {
		return id
	}
	return u.unkID
}
