//go:build windows

package imbhgo

// link_windows.go — cgo link directive for Windows (x86_64-pc-windows-gnu). Selects the combined
// libimbhgo.a and the Win32 system libs its Rust runtime needs (sockets, crypto, user env, etc.).
//
// See link_linux.go for the ${SRCDIR} vs CGO_LDFLAGS search-path rationale. Windows support is
// best-effort (see plan): the exact system-lib set is finalized empirically on a windows runner.

// #cgo LDFLAGS: -L${SRCDIR}/rust/target/release -limbhgo -lws2_32 -lbcrypt -luserenv -lntdll -ladvapi32
import "C"
