// SPDX-License-Identifier: Apache-2.0

package bert

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Metadata keys written by research/build_bert_graph.py and export_bert.py.
const (
	MetaWeightsFile   = "triage.weights.file"
	MetaWeightsSHA256 = "triage.weights.sha256"
	MetaWeightsSize   = "triage.weights.size"
	MetaVariant       = "triage.variant"
)

// ErrInvalidModel is returned for an ONNX file that cannot be parsed or whose
// triage metadata is missing or inconsistent with the request.
var ErrInvalidModel = errors.New("bert: invalid model file")

// ONNX ModelProto field numbers and protobuf wire types used by the reader.
const (
	fieldMetadataProps = 14
	fieldEntryKey      = 1
	fieldEntryValue    = 2

	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireI32    = 5

	// maxEntryLen bounds one metadata entry; real entries are below 200 bytes.
	maxEntryLen = 1 << 20
)

// ReadMetadataFile returns the metadata_props of the ONNX model at path. The
// file is streamed: the graph and initializers are skipped, never buffered.
func ReadMetadataFile(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is operator configuration or the artifact cache.
	if err != nil {
		return nil, fmt.Errorf("bert: open model: %w", err)
	}
	defer func() { _ = f.Close() }()
	meta, err := ReadMetadata(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return meta, nil
}

// ReadMetadata returns the metadata_props (ModelProto field 14) of a
// serialised ONNX model. Later duplicates of a key win, as in onnx.
func ReadMetadata(r io.Reader) (map[string]string, error) {
	br := bufio.NewReader(r)
	meta := map[string]string{}
	for {
		num, wt, err := readTag(br)
		if errors.Is(err, io.EOF) {
			return meta, nil
		}
		if err != nil {
			return nil, err
		}
		if num != fieldMetadataProps || wt != wireLen {
			if err := skipField(br, wt); err != nil {
				return nil, err
			}
			continue
		}
		n, err := readLen(br, maxEntryLen)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, fmt.Errorf("%w: truncated metadata entry", ErrInvalidModel)
		}
		k, v, err := parseEntry(buf)
		if err != nil {
			return nil, err
		}
		meta[k] = v
	}
}

func readTag(br *bufio.Reader) (num uint64, wt int, err error) {
	tag, err := binary.ReadUvarint(br)
	if errors.Is(err, io.EOF) {
		return 0, 0, io.EOF
	}
	if err != nil {
		return 0, 0, fmt.Errorf("%w: bad field tag: %w", ErrInvalidModel, err)
	}
	if tag>>3 == 0 {
		return 0, 0, fmt.Errorf("%w: field number 0", ErrInvalidModel)
	}
	return tag >> 3, int(tag & 7), nil
}

func readLen(br *bufio.Reader, limit uint64) (int64, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return 0, fmt.Errorf("%w: bad length: %w", ErrInvalidModel, unexpected(err))
	}
	if n > limit {
		return 0, fmt.Errorf("%w: field length %d exceeds %d", ErrInvalidModel, n, limit)
	}
	return int64(n), nil //nolint:gosec // G115: n <= limit <= MaxInt64.
}

func skipField(br *bufio.Reader, wt int) error {
	var n int64
	switch wt {
	case wireVarint:
		if _, err := binary.ReadUvarint(br); err != nil {
			return fmt.Errorf("%w: bad varint: %w", ErrInvalidModel, unexpected(err))
		}
		return nil
	case wireI64:
		n = 8
	case wireI32:
		n = 4
	case wireLen:
		var err error
		if n, err = readLen(br, 1<<62); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported wire type %d", ErrInvalidModel, wt)
	}
	if _, err := br.Discard(int(n)); err != nil {
		return fmt.Errorf("%w: truncated field: %w", ErrInvalidModel, unexpected(err))
	}
	return nil
}

// parseEntry decodes a StringStringEntryProto.
func parseEntry(b []byte) (key, value string, err error) {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 == 0 {
			return "", "", fmt.Errorf("%w: bad metadata entry", ErrInvalidModel)
		}
		b = b[n:]
		if tag&7 != wireLen {
			return "", "", fmt.Errorf("%w: metadata entry field %d has wire type %d", ErrInvalidModel, tag>>3, tag&7)
		}
		l, n := binary.Uvarint(b)
		if n <= 0 || l > uint64(len(b)-n) { //nolint:gosec // G115: 0 < n <= len(b).
			return "", "", fmt.Errorf("%w: truncated metadata entry", ErrInvalidModel)
		}
		s := string(b[n : n+int(l)]) //nolint:gosec // G115: l <= len(b).
		b = b[n+int(l):]             //nolint:gosec // G115: as above.
		switch tag >> 3 {
		case fieldEntryKey:
			key = s
		case fieldEntryValue:
			value = s
		}
	}
	return key, value, nil
}

// unexpected turns a bare EOF inside a field into io.ErrUnexpectedEOF.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
