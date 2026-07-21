//go:build darwin

package imbhgo

// link_darwin.go — cgo link directive for macOS. Selects the combined libimbhgo.a and the system
// libs its Rust runtime needs. macOS has no -lrt/-ldl (those live in libSystem); CoreFoundation and
// Security are pulled in by Rust std / imbh's TLS-capable deps.
//
// See link_linux.go for the ${SRCDIR} vs CGO_LDFLAGS search-path rationale. The exact -framework set
// is finalized empirically on a macOS runner; adjust if the link reports missing symbols.

// #cgo LDFLAGS: -L${SRCDIR}/rust/target/release -limbhgo -lpthread -lm -framework CoreFoundation -framework Security
import "C"
