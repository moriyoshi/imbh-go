package imbhgo

// debug.go — leak-gate accessors into the Rust side. cgo directives (CFLAGS/LDFLAGS) are collected
// package-wide from db.go, so this file only needs the header include.

// #include "imbhgo.h"
import "C"

// liveBatches is the number of live FfiBatch shells (created − freed). It returns to 0 once every
// cursor is drained/closed; a positive residual means a leaked shell, a negative one a double free.
func liveBatches() int64 { return int64(C.imbhgo_live_batches()) }

// liveDBs is the number of open Db handles in the registry.
func liveDBs() uint64 { return uint64(C.imbhgo_live_dbs()) }

// pendingQueryErrors is the number of un-fetched terminal query errors still held Rust-side.
func pendingQueryErrors() uint64 { return uint64(C.imbhgo_pending_query_errors()) }
