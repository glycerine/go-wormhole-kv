#!/usr/bin/env python3
"""Pinned, sequential C/Go load throughput and per-Put latency comparison.

Run from any directory. Native sources/optimization flags remain unchanged;
only the temporary concbench driver gains timing and skips probe rounds.
"""
import argparse
from pathlib import Path

from profile_compare import ROOT, native_driver, run


def build_native(out, clocked, safe, workers, workload, hot_keys=0):
    source = (ROOT / "concbench.c").read_text()
    source = source.replace("atomic_uint_least64_t __seqno = 0;", """
static u64 *latencies;
static int latency_cmp(const void *a, const void *b) {
  const u64 x = *(const u64 *)a, y = *(const u64 *)b;
  return (x > y) - (x < y);
}
static int sample_cmp(const void *a, const void *b) {
  return kv_compare(*(struct kv * const *)a, *(struct kv * const *)b);
}
atomic_uint_least64_t __seqno = 0;""")
    put = "whsafe_put" if safe else "wormhole_put"
    source = source.replace("  __nth = 4;", f"  __nth = {workers};")
    source = source.replace("thread_fork_join(4, (void *)kv_load_worker", f"thread_fork_join({workers}, (void *)kv_load_worker")
    if hot_keys:
        # Match Go's repeated-key trace and discard unused corpus records
        # before timing. Aliased samples must be freed only once at shutdown.
        source = source.replace("  // load (4)", f"""
  for (u64 i = {hot_keys}; i < nkeys; i++) {{
    free(samples[i]);
    samples[i] = samples[i % {hot_keys}];
  }}
  // load (4)""")
        cleanup = "  for (u64 i = 0; i < nkeys; i++)\n    free(samples[i]);"
        assert source.count(cleanup) == 1
        source = source.replace(cleanup, f"  for (u64 i = 0; i < {hot_keys}; i++)\n    free(samples[i]);")
        verify = f"""
  // Verify contents and cardinality after timing, including the fresh trace.
  struct wormref *verify = wormhole_ref(wh);
  for (u64 i = 0; i < {hot_keys}; i++) {{
    struct kref key;
    kref_ref_kv(&key, samples[i]);
    if (!wormhole_probe(verify, &key)) abort();
  }}
  struct wormhole_iter *it = wormhole_iter_create(verify);
  wormhole_iter_seek(it, kref_null());
  u64 distinct = 0;
  while (wormhole_iter_valid(it)) {{
    distinct++;
    wormhole_iter_skip1(it);
  }}
  wormhole_iter_destroy(it);
  wormhole_unref(verify);
  if (distinct != {hot_keys}) abort();
  printf("hot trace operations=%lu distinct=%lu verified=true\\n", nkeys, distinct);
"""
        source = source.replace("  const u64 nth =", verify + "  const u64 nth =")
    if workload == "ascending":
        source = source.replace("  // load (4)", "  qsort(samples, nkeys, sizeof(*samples), sample_cmp);\n  // load (4)")
    if workload == "update":
        source = source.replace("  __nth =", f"  struct wormref *preload = wormhole_ref(wh);\n  for (u64 i = 0; i < nkeys; i++)\n    if (!{put}(preload, samples[i])) abort();\n  wormhole_unref(preload);\n  __nth =")
    old = "  for (u64 i = n0; i < nz; i++)\n    wormhole_put(ref, __samples[i]);"
    if clocked:
        new = f"""  for (u64 i = n0; i < nz; i++) {{
    const u64 before = time_nsec();
    if (!{put}(ref, __samples[i])) abort();
    latencies[i] = time_nsec() - before;
  }}"""
    else:
        new = f"  for (u64 i = n0; i < nz; i++)\n    if (!{put}(ref, __samples[i])) abort();"
    assert source.count(old) == 1
    source = source.replace(old, new)
    source = source.replace("  __samples = samples;", "  latencies = calloc(nkeys, sizeof(*latencies));\n  __samples = samples;" if clocked else "  __samples = samples;")
    old = '  printf("load x4 %.2lf mops\\n", ((double)nkeys) * 1e3 / ((double)dtl));'
    assert source.count(old) == 1
    new = f'  printf("load x{workers} %.2lf mops count=%lu dt_ns=%lu clocked={str(clocked).lower()} workload={workload}\\n", ((double)nkeys) * 1e3 / ((double)dtl), nkeys, dtl);'
    if clocked:
        new += """
  qsort(latencies, nkeys, sizeof(*latencies), latency_cmp);
  printf("load latency ns count=%lu p50=%lu p95=%lu p99=%lu p999=%lu max=%lu (clock overhead included)\\n",
    nkeys, latencies[(nkeys-1)*500/1000], latencies[(nkeys-1)*950/1000],
    latencies[(nkeys-1)*990/1000], latencies[(nkeys-1)*999/1000], latencies[nkeys-1]);
  u32 control_cpus[CPU_SETSIZE];
  if (!process_getaffinity_list(CPU_SETSIZE, control_cpus)) abort();
  thread_pin(control_cpus[0]);
  const u64 control_n = nkeys < 100000 ? nkeys : 100000;
  const u64 control_start = time_nsec();
  for (u64 i = 0; i < control_n; i++) {
    const u64 before = time_nsec();
    latencies[i] = time_nsec() - before;
  }
  const u64 control_elapsed = time_nsec() - control_start;
  qsort(latencies, control_n, sizeof(*latencies), latency_cmp);
  printf("clock control cpu=%u count=%lu ns/pair=%.2lf p50=%lu p99=%lu (not subtracted)\\n",
    control_cpus[0], control_n, (double)control_elapsed / control_n,
    latencies[(control_n-1)/2], latencies[(control_n-1)*99/100]);
  free(latencies);"""
    source = source.replace(old, new)
    # native_driver accepts a driver-source override, not a library override.
    return native_driver(out, False, True, source=source)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--words", type=Path, default=ROOT / "words.txt")
    parser.add_argument("--cpus", default="0", help="CPU affinity for the single writer")
    parser.add_argument("--keys", type=int, default=1000000)
    parser.add_argument("--repeat", type=int, default=5)
    parser.add_argument("--safe", action="store_true", help="use native automatically parked whsafe_put")
    parser.add_argument("--workers", type=int, choices=[1], default=1,
                        help="evaluations use one writer")
    parser.add_argument("--workload", choices=["fresh", "update", "ascending"], default="fresh")
    parser.add_argument("--hot-keys", type=int, default=0, help="diagnostic: repeat the first N corpus keys (0 disables)")
    parser.add_argument("--go-binary", type=Path, help="compare an already built Go concbench")
    args = parser.parse_args()
    if min(args.keys, args.repeat, args.workers) <= 0:
        parser.error("keys, repeat and workers must be positive")
    if not 0 <= args.hot_keys <= args.keys:
        parser.error("hot-keys must be between zero and keys")
    if args.hot_keys and args.workload == "ascending":
        parser.error("hot-keys cannot be combined with ascending")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    go = args.go_binary.resolve() if args.go_binary else out / "go"
    if not args.go_binary:
        run(["go", "-C", ROOT / "go", "build", "-o", go, "./cmd/concbench"])
    native = {}
    for clocked in [False, True]:
        directory = out / ("clocked" if clocked else "unclocked")
        directory.mkdir(exist_ok=True)
        native[clocked] = build_native(directory, clocked, args.safe, args.workers, args.workload, args.hot_keys)
    with (out / "runs.txt").open("w", buffering=1) as log:
        log.write(f"CPUs={args.cpus} keys={args.keys} workers={args.workers} workload={args.workload} hot_keys={args.hot_keys} native_safe={args.safe} Go={go}\n")
        for clocked in [False, True]:
            for repeat in range(args.repeat):
                order = ["native", "go"] if repeat % 2 == 0 else ["go", "native"]
                for language in order:
                    log.write(f"variant={language} clocked={clocked} repeat={repeat}\n")
                    command = ["taskset", "-c", args.cpus, go if language == "go" else native[clocked]]
                    if language == "go":
                        command += ["-load-only", "-load-workers", args.workers, "-load-workload", args.workload]
                        if args.hot_keys:
                            command += ["-hot-keys", args.hot_keys]
                        if clocked:
                            command += ["-load-latency"]
                    command += [args.words.resolve(), args.keys, args.workers]
                    run(command, stdout=log)
                    print(f"completed {language} clocked={clocked} repeat={repeat}", flush=True)


if __name__ == "__main__":
    main()
