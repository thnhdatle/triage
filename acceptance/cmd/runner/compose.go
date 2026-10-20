// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/thnhdatle/triage/acceptance"
)

const (
	composeTimeout = 15 * time.Minute
	buildTimeout   = 60 * time.Minute
	buildEnvVar    = "TRIAGE_ACCEPTANCE_BUILD"
	ownService     = "triage"
)

// Criterion kinds (the kind field of criteria.yaml) that change how the stack is prepared.
const (
	kindPerformance = "performance"
	kindEvaluation  = "evaluation"
)

func composeExists() bool {
	_, err := os.Stat(composeFile)
	return err == nil
}

// buildStep builds binaries and images. A failing `make build` is a FAIL row; a failing docker
// build (or an overridden build command) is recorded as Blocked/FAIL without aborting the run.
func buildStep(w io.Writer) []Row {
	if cmd := os.Getenv(buildEnvVar); cmd != "" {
		if err := runCmd(buildTimeout, w, nil, "sh", "-c", cmd); err != nil {
			return []Row{{ID: "build", Status: StatusFail, Measured: err.Error(), Threshold: "-"}}
		}
		return nil
	}
	var rows []Row
	if err := runCmd(buildTimeout, w, nil, "make", "build"); err != nil {
		rows = append(rows, Row{ID: "build", Status: StatusFail, Measured: err.Error(), Threshold: "-"})
	}
	if err := runCmd(buildTimeout, w, nil, "make", "docker"); err != nil {
		rows = append(rows, Row{ID: "docker-build", Status: StatusBlocked, Threshold: "-", Note: err.Error()})
	}
	return rows
}

// Compose image variables and the local images `make docker` builds; the compose file defaults to
// the released images, the suite tests the working tree.
var localImages = [][2]string{
	{"RAYLD_TRIAGE_IMAGE", "thnhdatle/triage:dev"},
	{"RAYLD_FAKELLM_IMAGE", "thnhdatle/fakellm:dev"},
}

// useLocalImages points the compose stack (and every command the runner starts) at the local
// images unless the variables are already set.
func useLocalImages() {
	for _, kv := range localImages {
		if os.Getenv(kv[0]) == "" {
			_ = os.Setenv(kv[0], kv[1])
		}
	}
}

// composeUp brings the stack up when deploy/docker-compose.yml exists; failure is returned as a Row.
func composeUp(w io.Writer) []Row {
	if !composeExists() {
		say(w, "compose: %s not present, skipping stack start\n", composeFile)
		return nil
	}
	if err := runCmd(composeTimeout, w, nil, "docker", "compose", "-f", composeFile, "up", "-d", "--wait"); err != nil {
		return []Row{{ID: "compose-up", Status: StatusBlocked, Threshold: "-", Note: err.Error()}}
	}
	return nil
}

func composeServices() ([]string, error) {
	var buf bytes.Buffer
	cmd := exec.Command("docker", "compose", "-f", composeFile, "config", "--services") //nolint:gosec,noctx // fixed args
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("compose config --services: %w", err)
	}
	return strings.Fields(buf.String()), nil
}

// servicesToStop returns the services to stop before running c: everything not in real_deps for
// lmstudio criteria, only the project's own service for performance and evaluation criteria, which
// run the router in process and need the host's CPU.
func servicesToStop(c *acceptance.Criterion, all []string) []string {
	if slices.Contains(c.Requires, reqLMStudio) {
		var stop []string
		for _, s := range all {
			if !slices.Contains(c.RealDeps, s) {
				stop = append(stop, s)
			}
		}
		return stop
	}
	if (c.Kind == kindPerformance || c.Kind == kindEvaluation) && slices.Contains(all, ownService) {
		return []string{ownService}
	}
	return nil
}

// prepareStack stops services that would contend for resources with c.
func prepareStack(c *acceptance.Criterion, w io.Writer) {
	if !composeExists() {
		return
	}
	all, err := composeServices()
	if err != nil {
		say(w, "compose: %v\n", err)
		return
	}
	stop := servicesToStop(c, all)
	if len(stop) == 0 {
		return
	}
	args := append([]string{"compose", "-f", composeFile, "stop"}, stop...)
	if err := runCmd(composeTimeout, w, nil, "docker", args...); err != nil {
		say(w, "compose: %v\n", err)
	}
}
