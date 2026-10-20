// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/wire"
)

const (
	trg10ID        = "TRG-10"
	trg10NoMF      = "not measured (requires openai)"
	trg10ResultMD  = "out/eval/trg10.md"
	trg10OpenAIEnv = "OPENAI_API_KEY"
)

// trg10Row is one strategy row of the summary table of `triage eval`.
type trg10Row struct {
	threshold, strongRatio, accuracy, random, apgr, apgrLo, apgrHi float64
}

// trg10Run runs one committed eval config (labels mode, no LLM calls) in process with the
// strategies random, length and bert (mf only with an OpenAI key) and returns the summary
// rows by strategy name.
func trg10Run(t *testing.T, cfgPath string, withMF bool) map[string]trg10Row {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, cfgPath)) //nolint:gosec // G304: committed config
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	strategies := []string{"random", "length", "bert"}
	if withMF {
		strategies = append(strategies, "mf")
	}
	doc["strategies"] = strategies
	doc["config"] = filepath.Join(root, doc["config"].(string))
	ds := doc["dataset"].(map[string]any)
	sets := ds["sets"].([]any)
	for i, s := range sets {
		sets[i] = filepath.Join(root, s.(string))
	}
	out := t.TempDir()
	doc["out_dir"] = filepath.Join(out, "run")
	data, err := yaml.Marshal(doc)
	require.NoError(t, err)
	cfg := filepath.Join(out, "eval.yaml")
	require.NoError(t, os.WriteFile(cfg, data, 0o600))

	var stdout, stderr bytes.Buffer
	cli := &wire.CLI{Stdout: &stdout, Stderr: &stderr}
	code := cli.Run(t.Context(), []string{"eval", "--config", cfg})
	require.Zero(t, code, "triage eval %s: %s", cfgPath, stderr.String())
	return trg10Parse(t, stdout.String())
}

// trg10Parse reads the strategy rows of the summary table. A row of a strategy that could not
// be measured has its error in the threshold column and is left out.
func trg10Parse(t *testing.T, summary string) map[string]trg10Row {
	t.Helper()
	rows := map[string]trg10Row{}
	for _, line := range strings.Split(summary, "\n") {
		cells := strings.Split(strings.Trim(line, "| "), "|")
		if len(cells) != 8 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		th, err := strconv.ParseFloat(cells[1], 64)
		if err != nil {
			continue
		}
		var r trg10Row
		r.threshold = th
		r.strongRatio = trg10Float(t, strings.TrimSuffix(cells[2], "%")) / 100
		_, err = fmt.Sscanf(cells[3], "%f", &r.accuracy)
		require.NoError(t, err)
		r.random = trg10Float(t, cells[4])
		_, err = fmt.Sscanf(cells[7], "%f (%f-%f)", &r.apgr, &r.apgrLo, &r.apgrHi)
		require.NoError(t, err, "apgr cell %q", cells[7])
		rows[cells[0]] = r
	}
	return rows
}

func trg10Float(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	require.NoError(t, err, "number %q", s)
	return v
}

// TestAcceptance_TRG10_StrategiesBeatRandom implements TRG-10.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG10_StrategiesBeatRandom(t *testing.T) {
	th := thresholds(t, trg10ID)
	minAPGR, ok := th["gsm8k_apgr_bert_min"].(float64)
	require.True(t, ok, "gsm8k_apgr_bert_min threshold: %v", th["gsm8k_apgr_bert_min"])
	withMF := os.Getenv(trg10OpenAIEnv) != ""

	gsm := trg10Run(t, "eval/trg10_gsm8k.yaml", withMF)
	mmlu := trg10Run(t, "eval/trg10.yaml", withMF)
	gb, ok := gsm["bert"]
	require.True(t, ok, "GSM8K bert row missing")
	mb, ok := mmlu["bert"]
	require.True(t, ok, "MMLU bert row missing")

	mfNote := func(m map[string]trg10Row) string {
		if r, ok := m["mf"]; ok {
			return fmt.Sprintf("%.3f", r.apgr)
		}
		return trg10NoMF
	}
	var md strings.Builder
	md.WriteString("# TRG-10 offline routing quality (RouteLLM labels)\n\n")
	md.WriteString("Mode labels, strategy bert INT8, no LLM calls. APGR with 95% bootstrap CI.\n\n")
	md.WriteString("| set | bert | random | length | mf |\n|---|---|---|---|---|\n")
	for _, s := range []struct {
		name string
		m    map[string]trg10Row
	}{{"GSM8K (1,319 minus contaminated)", gsm}, {"MMLU (2,000 sample)", mmlu}} {
		b := s.m["bert"]
		fmt.Fprintf(&md, "| %s | %.3f (%.3f-%.3f) | %.3f | %.3f | %s |\n",
			s.name, b.apgr, b.apgrLo, b.apgrHi, s.m["random"].apgr, s.m["length"].apgr, mfNote(s.m))
	}
	fmt.Fprintf(&md, "\nGate: GSM8K bert APGR >= %.2f.\n", minAPGR)
	path := filepath.Join("..", trg10ResultMD)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(md.String()), 0o600))

	pass := gb.apgr >= minAPGR
	details := fmt.Sprintf("GSM8K APGR bert %.3f (min %.2f), random %.3f, length %.3f, mf %s; "+
		"MMLU APGR bert %.3f, random %.3f, length %.3f, mf %s",
		gb.apgr, minAPGR, gsm["random"].apgr, gsm["length"].apgr, mfNote(gsm),
		mb.apgr, mmlu["random"].apgr, mmlu["length"].apgr, mfNote(mmlu))
	t.Log(details)
	writeResult(t, Result{
		ID: trg10ID, Pass: pass, Details: details,
		Measured: map[string]any{
			"gsm8k_apgr_bert": gb.apgr, "gsm8k_apgr_random": gsm["random"].apgr,
			"gsm8k_apgr_length": gsm["length"].apgr, "gsm8k_apgr_mf": mfNote(gsm),
			"mmlu_apgr_bert": mb.apgr, "mmlu_apgr_random": mmlu["random"].apgr,
			"mmlu_apgr_length": mmlu["length"].apgr, "mmlu_apgr_mf": mfNote(mmlu),
		},
	})
	require.True(t, pass, details)
}
