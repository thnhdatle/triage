// SPDX-License-Identifier: Apache-2.0

package bert_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/domain/artifact"
)

type scriptRunner struct {
	fail  map[string]bool
	calls []string
	conv  *fakeConverter
	dir   string
}

func (s *scriptRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	line := name + " " + strings.Join(args, " ")
	if len(args) > 0 && (args[0] == "image" || args[0] == "pull") {
		s.calls = append(s.calls, line)
		if s.fail[line] {
			return errors.New("exit status 1")
		}
		return nil
	}
	s.calls = append(s.calls, "run")
	s.conv.args = args
	out := filepath.Join(s.dir, bert.INT8FileName)
	if err := os.WriteFile(out, s.conv.body, 0o600); err != nil {
		return err
	}
	_, err := io.WriteString(stdout, sha(s.conv.body)+"  "+out+"\n")
	return err
}

func TestImageResolver(t *testing.T) {
	t.Parallel()
	rel, dev := "ghcr.io/thnhdatle/triage-models", bert.DevImage
	r := bert.ImageResolver{Images: []string{rel, dev}}
	tests := []struct {
		name      string
		fail      []string
		wantImage string
		wantCalls []string
		wantErr   bool
	}{
		{
			name: "released image present", wantImage: rel,
			wantCalls: []string{"docker image inspect " + rel},
		},
		{
			name: "released image pulled", fail: []string{"docker image inspect " + rel}, wantImage: rel,
			wantCalls: []string{"docker image inspect " + rel, "docker pull " + rel},
		},
		{
			name: "dev fallback", fail: []string{"docker image inspect " + rel, "docker pull " + rel}, wantImage: dev,
			wantCalls: []string{"docker image inspect " + rel, "docker pull " + rel, "docker image inspect " + dev},
		},
		{
			name:    "neither",
			fail:    []string{"docker image inspect " + rel, "docker pull " + rel, "docker image inspect " + dev},
			wantErr: true,
			wantCalls: []string{
				"docker image inspect " + rel, "docker pull " + rel, "docker image inspect " + dev,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := &scriptRunner{fail: map[string]bool{}}
			for _, f := range tc.fail {
				run.fail[f] = true
			}
			conv, err := r.Resolve(t.Context(), run)
			assert.Equal(t, tc.wantCalls, run.calls)
			if tc.wantErr {
				require.ErrorIs(t, err, bert.ErrConvert)
				assert.Contains(t, err.Error(), rel)
				assert.Contains(t, err.Error(), dev)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, bert.ImageConverter{Image: tc.wantImage}, conv)
		})
	}
}

func TestINT8DeriverImageFallback(t *testing.T) {
	t.Parallel()
	rel := "ghcr.io/thnhdatle/triage-models"
	dir := t.TempDir()
	run := &scriptRunner{
		dir:  dir,
		fail: map[string]bool{"docker image inspect " + rel: true, "docker pull " + rel: true},
		conv: &fakeConverter{body: readFixture(t, bert.INT8FileName)},
	}
	d, err := bert.NewINT8Deriver(bert.ImageResolver{Images: []string{rel, bert.DevImage}},
		bert.WithCommandRunner(run))
	require.NoError(t, err)
	got, err := d.Derive(t.Context(), artifact.Entry{Name: "bert"}, dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, bert.INT8FileName), got.Path)
	assert.Contains(t, run.conv.args, bert.DevImage)
}
