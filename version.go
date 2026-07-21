package imbhgo

import "github.com/moriyoshi/imbh-go/internal/release"

// Version is the module release this source corresponds to. It is bumped in lockstep with each
// tagged release, and the prebuilt libimbhgo.a assets on GitHub Releases are named after it. The
// canonical value lives in internal/release (which is cgo-free so cmd/imbhgo-fetch can share it
// without depending on the archive); this re-export lets consumers read imbhgo.Version at runtime.
const Version = release.Version
