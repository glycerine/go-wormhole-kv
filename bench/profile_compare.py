#!/usr/bin/env python3
"""Matched native/Go perf capture; run from any directory (see PROFILE.md)."""
import argparse
from collections import Counter
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def run(argv, **kwargs):
    return subprocess.run([str(a) for a in argv], check=True, **kwargs)


def native_driver(out, safe, load_only, source=None):
    if source is None:
        source = (ROOT / "concbench.c").read_text()
        # Match the Go fixture's single-writer load for both profiling phases.
        source = source.replace("  __nth = 4;", "  __nth = 1;")
        source = source.replace("thread_fork_join(4, (void *)kv_load_worker",
                                "thread_fork_join(1, (void *)kv_load_worker")
    if safe:
        old = "if (wormhole_probe(ref, &kref))"
        assert source.count(old) == 1
        source = source.replace(old, "if (whsafe_probe(ref, &kref))")
    if load_only:
        old = "for (u64 i = 0; i < 3; i++) {"
        assert source.count(old) == 1
        source = source.replace(old, "for (u64 i = 0; i < 0; i++) {")
    old = 'printf("probe x%lu %.2lf mops\\n", nth, mops);'
    assert source.count(old) == 1
    source = source.replace(old, 'printf("probe x%lu %.2lf mops count=%lu dt_ns=%lu\\n", nth, mops, (u64)__tot, dt);')
    path = out / "native-driver.c"
    path.write_text(source)
    binary = out / "native"
    # Match Makefile.common's default optimized build, retaining debug info/LTO.
    run(["clang", "-std=gnu18", "-march=native", "-mtune=native", "-pthread",
         "-DNDEBUG", "-g3", "-O3", "-flto", "-fno-stack-protector",
         "-fno-builtin-memcpy", "-fno-builtin-memmove", "-fno-builtin-memcmp",
         "-rdynamic", "-I", ROOT, "-o", binary, path,
         ROOT / "lib.c", ROOT / "kv.c", ROOT / "wh.c", "-lm", "-lrt"])
    return binary


def summarize(data, binary, out, language):
    """Attribute each sample once; resolve C inlining without disabling it."""
    roots = {"load": "kv_load_worker" if language == "native" else "main.run.func1",
             "probe": "kv_probe_worker" if language == "native" else "main.run.func2"}
    nm = subprocess.check_output(["nm", "-n", str(binary)], text=True)
    symbols = {}
    for line in nm.splitlines():
        fields = line.split()
        if len(fields) == 3:
            try:
                symbols[fields[2]] = int(fields[0], 16)
            except ValueError:
                pass
    cache = data.with_suffix(".stacks")
    if cache.exists() and cache.stat().st_mtime >= data.stat().st_mtime:
        stacks = cache.read_text()
    else:
        stacks = subprocess.check_output(
            ["perf", "script", "--no-inline", "-i", str(data), "-F", "period,ip,sym,symoff"],
            text=True, stderr=subprocess.DEVNULL)
        cache.write_text(stacks)
    samples = []
    for block in stacks.strip().split("\n\n"):
        lines = block.strip().splitlines()
        if not lines or not lines[0].strip().isdigit():
            continue
        period = int(lines[0])
        phase = next((p for p, root in roots.items()
                      if any(re.search(re.escape(root) + r"\+0x", line)
                             for line in lines[1:])), "other")
        if len(lines) < 2:
            continue
        top = re.match(r"\s*[0-9a-f]+ (.+)\+0x([0-9a-f]+)$", lines[1])
        name = top[1] if top else lines[1].strip()
        addr = symbols[name] + int(top[2], 16) if top and name in symbols else None
        samples.append((phase, period, name, addr))
    addresses = sorted({addr for _, _, _, addr in samples if addr is not None})
    resolved = {}
    if addresses:
        query = "".join(f"{addr:x}\n" for addr in addresses)
        go_addr2line = os.environ.get("GO_ADDR2LINE")
        if language == "go" and not go_addr2line:
            tool_dir = subprocess.check_output(["go", "env", "GOTOOLDIR"], text=True).strip()
            candidate = Path(tool_dir) / "addr2line"
            if candidate.is_file():
                go_addr2line = str(candidate)
        if language == "go" and go_addr2line:
            lines = subprocess.check_output(
                [go_addr2line, str(binary)], input=query, text=True).splitlines()
            assert len(lines) == 2 * len(addresses)
            resolved = {addr: (lines[2*i], lines[2*i+1]) for i, addr in enumerate(addresses)}
        else:
            text = subprocess.check_output(
                ["addr2line", "-a", "-f", "-i", "-e", str(binary)], input=query, text=True)
            for block in re.split(r"(?m)^(?=0x[0-9a-f]+$)", text):
                lines = block.strip().splitlines()
                if len(lines) >= 3 and lines[0].startswith("0x"):
                    resolved[int(lines[0], 16)] = (lines[1], lines[2])
    report = [f"{language}: sampled user cycles, innermost inline function; no overlapping totals"]
    for phase in ["load", "probe", "other"]:
        counts, locations = Counter(), Counter()
        count = 0
        for p, period, name, addr in samples:
            if p != phase:
                continue
            count += 1
            function, source = resolved.get(addr, (name, "external/unknown"))
            counts[function] += period
            locations[(function, source)] += period
        total = sum(counts.values())
        report.append(f"\n{phase}: samples={count} estimated_cycles={total}")
        for function, cycles in counts.most_common(25):
            report.append(f"{100 * cycles / total:7.2f}% {cycles:14d} {function}")
        report.append("Hottest source locations (sampling skid applies):")
        for (function, source), cycles in locations.most_common(12):
            report.append(f"{100 * cycles / total:7.2f}% {function} {source}")
    out.write_text("\n".join(report) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--words", type=Path, default=ROOT / "words.txt")
    parser.add_argument("--cpus", default="0-3")
    parser.add_argument("--keys", type=int, default=1000000)
    parser.add_argument("--phase", choices=["load", "probe"], default="probe")
    parser.add_argument("--safe", action="store_true", help="native whsafe_probe")
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--frequency", type=int, default=499)
    parser.add_argument("--summarize-only", action="store_true")
    args = parser.parse_args()
    if min(args.keys, args.repeat, args.frequency) <= 0:
        parser.error("keys, repeat and frequency must be positive")
    args.out = args.out.resolve()
    args.out.mkdir(parents=True, exist_ok=True)
    if not args.summarize_only:
        native = native_driver(args.out, args.safe, args.phase == "load")
        go = args.out / "go"
        run(["go", "-C", ROOT / "go", "build", "-o", go, "./cmd/concbench"])
        for language, binary in [("native", native), ("go", go)]:
            with (args.out / f"{language}.log").open("w") as log:
                for repeat in range(args.repeat):
                    command = ["taskset", "-c", args.cpus, binary]
                    if language == "go":
                        command += ["-load-workers", "1"]
                    if language == "go" and args.phase == "load":
                        command.append("-load-only")
                    command += [args.words.resolve(), args.keys, "1" if args.phase == "load" else "4"]
                    # Go emits frame pointers; optimized C needs DWARF unwind.
                    graph = "fp" if language == "go" else "dwarf,16384"
                    run(["perf", "record", "-q", "-e", "cycles:u", "-F", args.frequency,
                         "--call-graph", graph, "-o",
                         args.out / f"{language}-{repeat}.perf", "--", *command], stdout=log)
    for language in ["native", "go"]:
        for repeat in range(args.repeat):
            summarize(args.out / f"{language}-{repeat}.perf", args.out / language,
                      args.out / f"{language}-{repeat}.txt", language)


if __name__ == "__main__":
    main()
