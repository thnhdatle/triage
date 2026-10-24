// SPDX-License-Identifier: Apache-2.0

// Package buildinfo exposes the version, commit and build date set at link time.
package buildinfo

import "runtime"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is the build information returned by GET /v1/version.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

func Get() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: Date, GoVersion: runtime.Version()}
}
