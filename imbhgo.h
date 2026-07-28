/*
 * imbhgo.h — C ABI for the combined imbhgo staticlib (binding plan M0).
 * Keep in sync with the #[unsafe(no_mangle)] fns in rust/src/lib.rs.
 */
#ifndef IMBHGO_H
#define IMBHGO_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Register handlers on sable (idempotent; call before sable's runtime is built). */
void imbhgo_init(void);

/* Every imbhgo_open* call takes an err_id: a caller-allocated id (same counter as query ids) under
 * which the failure's cause is stashed, to be fetched with the OP_QUERY_ERROR byte-Call. These are
 * direct C calls with only a u64 return, so without it a 0 handle carries no reason at all. Pass 0
 * to discard the cause. */

/* Open an ephemeral in-memory Db; returns its handle id (0 on error). */
uint64_t imbhgo_open_memory(uint64_t err_id);

/* Open an on-disk Db at the UTF-8 path [path, path+len); returns its handle id (0 on error). */
uint64_t imbhgo_open(const uint8_t *path, size_t len, uint64_t err_id);

/* Open an existing on-disk Db read-only at [path, path+len); returns its handle id (0 on error).
 * Coexists with the single writer and other readers. */
uint64_t imbhgo_open_read_only(const uint8_t *path, size_t len, uint64_t err_id);

/* Open a Db with JSON-encoded builder options at [opts, opts+len); returns its handle id (0 on error). */
uint64_t imbhgo_open_opts(const uint8_t *opts, size_t len, uint64_t err_id);

/* Close (drop) a Db handle. */
void imbhgo_close(uint64_t id);

/* Free a taken batch's shell allocation (its arrays are owned by Go's imported Record). */
void imbhgo_shell_free(uint64_t ptr);

/* Leak-gate accessors: live FfiBatch shells (0 when quiesced; <0 = double free), open Db handles,
 * and un-fetched terminal query errors. */
int64_t imbhgo_live_batches(void);
uint64_t imbhgo_live_dbs(void);
uint64_t imbhgo_pending_query_errors(void);

#ifdef __cplusplus
}
#endif

#endif /* IMBHGO_H */
