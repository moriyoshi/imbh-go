// Package release holds the release coordinates for the prebuilt libimbhgo.a archives: the version
// this source corresponds to and the naming/URL scheme for the assets published on GitHub Releases.
//
// It is deliberately dependency-free and cgo-free so the fetch tool (cmd/imbhgo-fetch) can be built
// and run WITHOUT the archive it is meant to fetch — the cgo-carrying imbhgo package must never be a
// build prerequisite of the fetcher. The root package re-exports Version from here (see version.go).
package release

import (
	"fmt"
	"strings"
)

// Version is the module release this source tree corresponds to, bumped in lockstep with each tag.
// The prebuilt assets are published under a GitHub release named after it (e.g. "v0.2.0").
const Version = "v0.2.0"

// DefaultBaseURL is the GitHub Releases download prefix. A concrete asset lives at
// <DefaultBaseURL>/<version>/<asset>. Overridable in the fetch tool (e.g. for a mirror or a local
// file server in tests).
const DefaultBaseURL = "https://github.com/moriyoshi/imbh-go/releases/download"

// SumsName is the checksum manifest published alongside the assets (GNU coreutils `sha256sum`
// format: "<hex>  <filename>" per line), covering the compressed .a.zst assets.
const SumsName = "SHA256SUMS"

// LibcGlibc and LibcMusl are the recognized Linux C library flavors. On non-Linux platforms the
// libc component is empty and omitted from the asset name.
const (
	LibcGlibc = "glibc"
	LibcMusl  = "musl"
)

// AssetName returns the compressed-archive file name for a target cell, e.g.
// "libimbhgo-v0.2.0-linux-amd64.a.zst" (glibc/default) or "libimbhgo-v0.2.0-linux-amd64-musl.a.zst".
// Only Linux musl carries a libc suffix; glibc is the default and other OSes have no libc axis.
func AssetName(version, goos, goarch, libc string) string {
	name := fmt.Sprintf("libimbhgo-%s-%s-%s", version, goos, goarch)
	if goos == "linux" && libc == LibcMusl {
		name += "-" + LibcMusl
	}
	return name + ".a.zst"
}

// AssetURL is the full download URL for a target cell's compressed archive.
func AssetURL(baseURL, version, goos, goarch, libc string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(baseURL, "/"), version, AssetName(version, goos, goarch, libc))
}

// SumsURL is the full download URL for the checksum manifest of a release.
func SumsURL(baseURL, version string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(baseURL, "/"), version, SumsName)
}

// CacheSubdir is the per-cell directory (relative to the cache root) where the decompressed
// libimbhgo.a is placed, e.g. "imbhgo/v0.2.0/linux-amd64" or ".../linux-amd64-musl".
func CacheSubdir(version, goos, goarch, libc string) string {
	cell := fmt.Sprintf("%s-%s", goos, goarch)
	if goos == "linux" && libc == LibcMusl {
		cell += "-" + LibcMusl
	}
	return fmt.Sprintf("imbhgo/%s/%s", version, cell)
}

// DefaultLibc reports the Linux C library flavor to target. It is file-based (no exec): the presence
// of a musl dynamic loader marks a musl system; everything else (including non-Linux) is treated as
// glibc/none. exists is injected so callers/tests can supply a probe; pass osStatExists in main.
func DefaultLibc(goos string, exists func(glob string) bool) string {
	if goos != "linux" {
		return ""
	}
	for _, g := range []string{"/lib/ld-musl-*.so.1", "/lib/libc.musl-*.so.1"} {
		if exists(g) {
			return LibcMusl
		}
	}
	return LibcGlibc
}
