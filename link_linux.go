//go:build linux

package imbhgo

// link_linux.go — cgo link directive for Linux (glibc and musl). Selects the combined
// libimbhgo.a (imbh + sable runtime + our handlers + the imbhgo_*/sable_* C ABI) and the system
// libs its Rust runtime needs.
//
// The -L${SRCDIR}/rust/target/release search dir is the local co-development default (populated by
// `make rust`). For a downstream consumer using a prebuilt archive that directory does not exist in
// the read-only module cache, so the linker ignores it and resolves -limbhgo from the -L supplied
// via CGO_LDFLAGS instead (see cmd/imbhgo-fetch and the README). Build with -tags sable_extern_lib.

// #cgo LDFLAGS: -L${SRCDIR}/rust/target/release -limbhgo -lpthread -lm -ldl
import "C"
