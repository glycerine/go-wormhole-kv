#!/usr/bin/env python3
"""Reproduce scan locality measurements using standard perf and the Go driver."""
import argparse
import json
import os
from pathlib import Path
import statistics
import subprocess
import sys
import tempfile

EVENTS = "{cycles:u,instructions:u,L1-dcache-loads:u,L1-dcache-load-misses:u}"
CASES = [
    ("tidwall / full ordered scan", "tidwall", "full", False),
    ("insdict / sorted inserts", "insdict", "ordered", True),
    ("insdict / unordered All", "insdict", "unordered", False),
]


def counters(text):
    result = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        fields = line.split(";")
        if len(fields) < 5:
            raise ValueError("malformed perf output: " + line)
        name = fields[2].removesuffix(":u")
        if name not in ("cycles", "instructions", "L1-dcache-loads", "L1-dcache-load-misses"):
            continue
        if float(fields[4]) < 99.99:
            raise ValueError("counter multiplexed or not running: " + line)
        result[name] = int(fields[0])
    if len(result) != 4 or min(result.values()) < 0 or result["L1-dcache-loads"] == 0 or result["cycles"] == 0:
        raise ValueError("missing or invalid perf counters")
    return result


def format_table(headers, rows):
    widths = [max(len(header), *(len(row[i]) for row in rows))
              for i, header in enumerate(headers)]

    def line(row):
        return "| " + " | ".join(
            cell.ljust(width) if i == 0 else cell.rjust(width)
            for i, (cell, width) in enumerate(zip(row, widths))) + " |"

    separator = ["-" * widths[0]] + ["-" * (width - 1) + ":" for width in widths[1:]]
    return [line(headers), line(separator), *(line(row) for row in rows)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpu", type=int, default=6)
    parser.add_argument("--count", type=int, default=1)
    parser.add_argument("--scans", type=int, default=20000)
    parser.add_argument("--n", type=int, default=65536)
    parser.add_argument("--output", type=Path, help="retain raw counters and driver JSON here")
    args = parser.parse_args()
    if min(args.count, args.scans, args.n) <= 0:
        parser.error("count, scans and n must be positive")
    if args.cpu < 0 or args.cpu not in os.sched_getaffinity(0):
        parser.error("cpu must be in the process's allowed affinity")
    module = Path(__file__).resolve().parent
    env = dict(os.environ, GOMAXPROCS="1", LC_ALL="C")
    with tempfile.TemporaryDirectory(prefix="wormhole-l1-") as tmp:
        work = Path(tmp)
        output = args.output.resolve() if args.output else work / "results"
        output.mkdir(parents=True, exist_ok=True)
        binary = work / "scanperf"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/scanperf"], cwd=module, env=env, check=True)
        measurements = [[] for _ in CASES]
        expected = {}
        # Alternate run order to reduce systematic thermal/frequency bias.
        for repeat in range(args.count):
            order = range(len(CASES)) if repeat % 2 == 0 else reversed(range(len(CASES)))
            for index in order:
                label, container, mode, ordered = CASES[index]
                run = work / f"run-{repeat}-{index}"
                run.mkdir()
                ctl, ack = run / "control", run / "ack"
                os.mkfifo(ctl)
                os.mkfifo(ack)
                # Full scans visit N entries; keep total work near the seek trace.
                scans = args.scans if mode == "ordered" else max(1, args.scans * 2 // 5)
                prefix = output / f"{repeat}-{index}"
                command = ["perf", "stat", "-x", ";", "-o", str(prefix)+".stat", "--delay=-1",
                           f"--control=fifo:{ctl},{ack}", "-e", EVENTS, "--", "taskset", "-c", str(args.cpu),
                           str(binary), "-cpu", str(args.cpu), "-container", container, "-mode", mode,
                           "-n", str(args.n), "-scans", str(scans), "-control", str(ctl), "-ack", str(ack)]
                if ordered:
                    command.append("-ordered-inserts")
                print(f"L1 sample {repeat+1}/{args.count}: {label}", file=sys.stderr, flush=True)
                proc = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
                Path(str(prefix)+".stderr").write_text(proc.stderr)
                if proc.returncode:
                    raise RuntimeError(f"perf failed ({proc.returncode}): {proc.stderr}")
                data = json.loads(proc.stdout)
                Path(str(prefix)+".json").write_text(proc.stdout)
                events = counters(Path(str(prefix)+".stat").read_text())
                visits, checksum = data["visited"], data["sum"]
                group = "ordered" if mode == "ordered" else "full"
                if mode != "ordered" and (visits, checksum) != (args.n * scans, args.n * (args.n-1) // 2 * scans):
                    raise ValueError("full traversal visit count/checksum mismatch")
                if visits <= 0 or (visits, checksum) != expected.setdefault(group, (visits, checksum)):
                    raise ValueError("scan visit count/checksum mismatch")
                measurements[index].append([
                    data["ns_per_key"],
                    100 * (1 - events["L1-dcache-load-misses"] / events["L1-dcache-loads"]),
                    events["L1-dcache-load-misses"] / visits,
                    events["instructions"] / visits,
                    events["instructions"] / events["cycles"],
                ])
        sampling = "one pass" if args.count == 1 else f"medians of {args.count} samples"
        lines = [f"CPU {args.cpu}; GOMAXPROCS=1; N={args.n}; {sampling}; setup excluded.", ""]
        headers = ["Container / traversal", "ns/key", "L1 hit estimate", "L1 misses/key", "Instructions/key", "Ins/cycle"]
        rows = []
        for case, samples in zip(CASES, measurements):
            ns, hit, misses, instructions, ipc = map(statistics.median, zip(*samples))
            rows.append([case[0], f"{ns:.2f}", f"{hit:.2f}%", f"{misses:.3f}", f"{instructions:.2f}", f"{ipc:.2f}"])
        lines += format_table(headers, rows)
        lines += ["", "L1 hit estimate = 100 * (1 - misses/accesses). On Zen 2 these counters include",
                  "load/store accesses and prefetch traffic; this is not an exact retired-load hit rate.",
                  "Ordered scans use the same trace; full traversals use the same visits/checksum."]
        table = "\n".join(lines) + "\n"
        (output / "table.txt").write_text(table)
        print(table, end="")
        if args.output:
            print(f"Raw measurements: {output}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        sys.exit(str(exc))
