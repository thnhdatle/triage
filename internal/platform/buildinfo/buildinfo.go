// SPDX-License-Identifier: Apache-2.0

// Package buildinfo exposes the version, commit and build date injected by the
// linker, plus the Go runtime version.
//
//	info := buildinfo.Get()
//	fmt.Println(info.Version)
package buildinfo

import "runtime"

// Linker-injected values (see Makefile LDFLAGS). They are constant tables, set once at link time.
var (
	// Version is the release version or "dev".
	Version = "dev"
	// Commit is the short git commit sha.
	Commit = "unknown"
	// Date is the UTC build date.
	Date = "unknown"
)

// Info is the build information returned by GET /v1/version.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Get returns the build information of the running binary.
func Get() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: Date, GoVersion: runtime.Version()}
}
