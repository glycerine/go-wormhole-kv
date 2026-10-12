#!/usr/bin/env python3
"""Sequential pinned baseline/candidate measurements for RECOVER_PERF.md.

Build isolated source snapshots before timing. Only the temporary driver's
probe seed changes; constructor settings and the operation loops are preserved.
Never run two copies concurrently. Outputs retain every command and raw result.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import statistics
import subprocess
import tarfile
import time

ROOT = Path(__file__).resolve().parents[2]


def cpu_snapshot():
    return {s[0]: list(map(int, s[1:9]))
            for line in Path('/proc/stat').read_text().splitlines()
            if (s := line.split()) and s[0].startswith('cpu')}


def cpu_delta(before, after):
    result = {}
    for cpu, old in before.items():
        delta = [b - a for a, b in zip(old, after[cpu])]
        total = sum(delta)
        result[cpu] = 100 * (total - delta[3] - delta[4]) / max(total, 1)
    return result


def cpu_busy():
    before = cpu_snapshot()
    time.sleep(1)
    return cpu_delta(before, cpu_snapshot())


def cpu_set(text):
    result = set()
    for part in text.split(','):
        limits = list(map(int, part.split('-')))
        result.update(range(limits[0], limits[-1] + 1))
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--baseline', required=True)
    p.add_argument('--binaries-from', type=Path,
                   help='reuse verified frozen binaries and patch from an earlier output directory')
    p.add_argument('--candidate', type=Path, default=ROOT)
    p.add_argument('--out', required=True, type=Path)
    p.add_argument('--repeat', type=int, default=3)
    p.add_argument('--duration', default='2s')
    p.add_argument('--cpu-one', default='0', help='single-worker CPU affinity list')
    p.add_argument('--cpu-four', default='0-3', help='four-reader CPU affinity list')
    p.add_argument('--cpu-twenty', default='0-19', help='twenty-reader CPU affinity list')
    p.add_argument('--benchtime', default='300ms')
    p.add_argument('--cases', default='.*', help='regular expression selecting screen case names')
    p.add_argument('--load-latency', action='store_true',
                   help='also clock each string Put and record latency quantiles (overhead included)')
    p.add_argument('--idle-limit', type=float, default=15,
                   help='abort before timing if target CPU or SMT sibling utilization exceeds this percent')
    p.add_argument('--host-idle-limit', type=float, default=15,
                   help='separate host-wide utilization limit; 100 allows background work on other CPUs')
    p.add_argument('--keys', type=int, default=1000000)
    p.add_argument('--work', required=True, type=Path)
    args = p.parse_args()
    if (args.repeat < 1 or args.keys < 1 or not 0 < args.idle_limit < 100
            or not 0 < args.host_idle_limit <= 100):
        p.error('positive repeat/keys and idle-limit between 0 and 100 required')
    out, work = args.out.resolve(), args.work.resolve()
    out.mkdir(parents=True, exist_ok=True)
    work.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOPROXY='off', GOMAXPROCS='1')
    records = []
    with (out / 'runs.txt').open('w', buffering=1) as log:
        def run(command, cwd=None):
            command = list(map(str, command))
            log.write(json.dumps({'command': command, 'cwd': str(cwd)}) + '\n')
            result = subprocess.run(command, cwd=cwd, env=env, text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            log.write(result.stdout + '\n')
            result.check_returncode()
            return result.stdout

        revision = run(['git', 'rev-parse', args.baseline], ROOT).strip()
        binaries = {}
        if args.binaries_from:
            origin = args.binaries_from.resolve()
            if origin == out:
                raise RuntimeError('frozen binary source and new output must differ')
            old_revision = (origin / 'runs.txt').read_text().splitlines()[1].strip()
            if old_revision != revision:
                raise RuntimeError('frozen baseline revision differs from --baseline')
            manifest = json.loads((origin / 'binaries.json').read_text())
            for label, paths in manifest.items():
                checked = {}
                for filename, digest in paths.items():
                    path = Path(filename)
                    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                        raise RuntimeError(f'frozen binary changed: {path}')
                    checked[path.name] = path
                binaries[label] = checked['concbench'], checked['bench.test']
            shutil.copyfile(origin / 'candidate.patch', out / 'candidate.patch')
            benchmark = origin / 'recover_prepared_bench_test.go.txt'
            if benchmark.exists():
                shutil.copyfile(benchmark, out / benchmark.name)
            (out / 'binaries.json').write_text(json.dumps(manifest, indent=2) + '\n')
            log.write(json.dumps({'frozen_binaries_from': str(origin),
                                  'hashes_verified': True}) + '\n')
        else:
            (out / 'candidate.patch').write_text(run(['git', 'diff', 'HEAD', '--', 'go'], args.candidate))
            binaries = {}
            for label in ['baseline', 'candidate']:
                source = work / label
                if source.exists():
                    raise RuntimeError(f'refusing to overwrite {source}; use a fresh --work directory')
                source.mkdir()
                if label == 'baseline':
                    archive = subprocess.check_output(['git', '-C', str(ROOT), 'archive', revision, 'go'])
                    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
                        tar.extractall(source, filter='data')
                else:
                    shutil.copytree(args.candidate / 'go', source / 'go',
                                    ignore=shutil.ignore_patterns('results', '__pycache__', '*.out'))
                driver = source / 'go/cmd/concbench/main.go'
                text = driver.read_text()
                original = 'master.Seed(uint64(time.Now().UnixNano()))'
                assert text.count(original) == 1
                driver.write_text(text.replace(original, 'master.Seed(2023)'))
                # Install identical prepared-key loops in both snapshots, including
                # baselines from before this diagnostic benchmark existed.
                prepared_bench = ROOT / 'go/bench/recover_prepared_bench_test.go'
                shutil.copyfile(prepared_bench, source / 'go/bench/recover_prepared_bench_test.go')
                shutil.copyfile(prepared_bench, out / 'recover_prepared_bench_test.go.txt')
                conc, bench = source / 'concbench', source / 'bench.test'
                run(['go', 'build', '-trimpath', '-o', conc, './cmd/concbench'], source / 'go')
                run(['go', 'test', '-trimpath', '-c', '-o', bench, '.'], source / 'go/bench')
                binaries[label] = conc, bench
            (out / 'binaries.json').write_text(json.dumps({
                label: {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in paths}
                for label, paths in binaries.items()}, indent=2) + '\n')

        cases = []
        for workers in [1, 4, 20]:
            cpus = {1: args.cpu_one, 4: args.cpu_four, 20: args.cpu_twenty}[workers]
            # All probe fixtures are loaded by one writer; reader concurrency varies.
            cases.append((f'probe/{workers}', cpus, 'conc',
                          ['-load-workers', 1, '-duration', args.duration, '-rounds', 3],
                          re.compile(r'probe x\d+ ([0-9.]+) mops'), True))
        for workload in ['fresh', 'ascending', 'update']:
            for workers in [1]:
                flags = ['-load-only', '-load-workers', workers, '-load-workload', workload]
                if workload == 'update':
                    flags += ['-hot-keys', 64]
                cases.append((f'{workload}/{workers}', args.cpu_one,
                              'conc', flags, re.compile(r'load x\d+ ([0-9.]+) mops'), True))
        for kind in ['uint64', 'string']:
            for cache in ['false', 'true']:
                cases.append((f'points/{kind}/cache={cache}', args.cpu_one, 'bench',
                              ['-test.run=^$', '-test.bench=^BenchmarkComparePoints$/^Wormhole$/',
                               f'-test.benchtime={args.benchtime}', '-test.count=1',
                               f'-bench-cpu={min(cpu_set(args.cpu_one))}',
                               f'-bench-key-type={kind}', f'-wormhole-meta-cache={cache}'],
                              re.compile(r'^(Benchmark\S+)\s+\d+\s+([0-9.]+) ns/op', re.M), False))

        for cache in ['false', 'true']:
            cases.append((f'prepared/cache={cache}', args.cpu_one, 'bench',
                          ['-test.run=^$', '-test.bench=^BenchmarkRecoverPrepared$/',
                           f'-test.benchtime={args.benchtime}', '-test.count=1',
                           f'-bench-cpu={min(cpu_set(args.cpu_one))}',
                           f'-wormhole-meta-cache={cache}'],
                          re.compile(r'^(Benchmark\S+)\s+\d+\s+([0-9.]+) ns/op', re.M), False))

        for name, cpus, binary, flags, pattern, higher in cases:
            if not re.search(args.cases, name):
                continue
            clocked = args.load_latency and binary == 'conc' and not name.startswith('probe/')
            case_name = name + ('/clocked' if clocked else '')
            for repeat in range(args.repeat):
                order = ['baseline', 'candidate'] if repeat % 2 == 0 else ['candidate', 'baseline']
                for label in order:
                    busy = cpu_busy()
                    observation = dict(case=name, variant=label, repeat=repeat,
                                       busy_percent=busy, time=time.time())
                    with (out / 'host-load.jsonl').open('a') as dest:
                        dest.write(json.dumps(observation) + '\n')
                    targets = cpu_set(cpus)
                    for cpu in list(targets):
                        siblings = Path(f'/sys/devices/system/cpu/cpu{cpu}/topology/thread_siblings_list')
                        if siblings.exists():
                            targets.update(cpu_set(siblings.read_text().strip()))
                    if busy['cpu'] > args.host_idle_limit or any(
                            busy[f'cpu{cpu}'] > args.idle_limit for cpu in targets):
                        raise RuntimeError('machine is busy before timing; raw partial results preserved; '
                                           'retry in a fresh --out/--work directory when idle')
                    command = ['taskset', '-c', cpus, binaries[label][binary == 'bench'], *flags]
                    if binary == 'conc':
                        if clocked:
                            command.append('-load-latency')
                        workers = name.split('/')[-1]
                        command += [ROOT / 'words.txt', args.keys, workers]
                    before_run = cpu_snapshot()
                    output = run(command)
                    during = cpu_delta(before_run, cpu_snapshot())
                    with (out / 'host-during.jsonl').open('a') as dest:
                        dest.write(json.dumps(dict(case=name, variant=label, repeat=repeat,
                                                   busy_percent=during, time=time.time())) + '\n')
                    # This taskset process cannot use SMT siblings outside its
                    # affinity. Their activity is external, including load that
                    # started after the preflight check. Target-CPU contention
                    # itself cannot be separated from our work via /proc/stat.
                    outside_siblings = targets - cpu_set(cpus)
                    if any(during[f'cpu{cpu}'] > args.idle_limit for cpu in outside_siblings):
                        raise RuntimeError('external SMT sibling load during timing; sample excluded; '
                                           'raw output retained; retry in fresh directories')
                    if binary == 'conc' and name.startswith('probe/'):
                        assert output.count('misses=0') == 3, output
                    matches = pattern.findall(output)
                    assert matches, output
                    groups = [(case_name, list(map(float, matches)))] if higher else [
                        (name + '/' + subname, [float(value)]) for subname, value in matches]
                    if clocked:
                        quantiles = re.search(r'load latency ns count=\d+ p50=(\d+) p95=(\d+) '
                                              r'p99=(\d+) p999=(\d+) max=(\d+)', output)
                        assert quantiles, output
                        for quantile, value in zip(['p50', 'p95', 'p99', 'p999', 'max'], quantiles.groups()):
                            record = dict(case=case_name + '/' + quantile, variant=label,
                                          repeat=repeat, values=[float(value)], higher_is_better=False)
                            records.append(record)
                            with (out / 'measurements.jsonl').open('a') as dest:
                                dest.write(json.dumps(record) + '\n')
                    for case, values in groups:
                        record = dict(case=case, variant=label, repeat=repeat, values=values,
                                      higher_is_better=higher)
                        records.append(record)
                        with (out / 'measurements.jsonl').open('a') as dest:
                            dest.write(json.dumps(record) + '\n')
                    print(f'{name} {repeat} {label}: {matches}', flush=True)
        lines = ['| Case | Baseline median | Candidate median | Change in performance |',
                 '| --- | ---: | ---: | ---: |']
        for case in dict.fromkeys(r['case'] for r in records):
            values = {label: [v for r in records if r['case'] == case and r['variant'] == label
                              for v in r['values']] for label in binaries}
            a, b = (statistics.median(values[label]) for label in binaries)
            higher = next(r['higher_is_better'] for r in records if r['case'] == case)
            gain = (b / a - 1) * 100 if higher else (a / b - 1) * 100
            lines.append(f'| {case} | {a:.2f} | {b:.2f} | {gain:+.2f}% |')
        summary = '\n'.join(lines) + '\n'
        (out / 'summary.md').write_text(summary)
        print(summary, flush=True)


if __name__ == '__main__':
    main()
