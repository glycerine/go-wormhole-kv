#!/usr/bin/env python3
"""Compare isolated revisions using concbench and the actual make bench target.

All child processes, including builds, inherit CPUs 17-30. Cases run serially,
with alternating baseline/candidate order. Four-reader fixtures use one loader;
fresh-write decisions use concbench with positional threads=1 and make bench.
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

ROOT = Path(__file__).resolve().parents[2]
ALLOWED = set(range(17, 31))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', required=True)
    parser.add_argument('--baseline-pgo', type=Path, help='explicit baseline profile for comparing two PGO builds')
    parser.add_argument('--candidate-pgo', type=Path, help='compare PGO-off baseline to this explicit candidate profile')
    parser.add_argument('--candidate-table-pgo', type=Path, help='separate profile trained across all table containers')
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--repeat', type=int, default=3)
    parser.add_argument('--benchtime', default='100ms')
    parser.add_argument('--table-procs', type=int, default=1, help='make bench GOMAXPROCS (default matches go/Makefile: 1)')
    parser.add_argument('--cases', default='.*', help='regex selecting fresh, latency, probe4, bench-uint64 or bench-string')
    args = parser.parse_args()
    if args.repeat < 1 or args.table_procs < 1:
        parser.error('repeat and table-procs must be positive')
    os.sched_setaffinity(0, ALLOWED)
    out, work = args.out.resolve(), args.work.resolve()
    out.mkdir(parents=True, exist_ok=False)
    work.mkdir(parents=True, exist_ok=False)
    base_flags = '-trimpath -buildvcs=false'
    if args.candidate_pgo or args.candidate_table_pgo:
        base_flags += ' -pgo=off'
    flags = {label: base_flags for label in ['baseline', 'candidate']}
    if args.baseline_pgo:
        shutil.copyfile(args.baseline_pgo.resolve(), out / 'baseline.pgo')
        flags['baseline'] = '-trimpath -buildvcs=false -pgo=' + str(out / 'baseline.pgo')
    if args.candidate_pgo:
        shutil.copyfile(args.candidate_pgo.resolve(), out / 'candidate.pgo')
        flags['candidate'] = '-trimpath -buildvcs=false -pgo=' + str(out / 'candidate.pgo')
    table_flags = dict(flags)
    if args.candidate_table_pgo:
        shutil.copyfile(args.candidate_table_pgo.resolve(), out / 'candidate-table.pgo')
        table_flags['candidate'] = '-trimpath -buildvcs=false -pgo=' + str(out / 'candidate-table.pgo')
    env = dict(os.environ, GOPROXY='off', GOFLAGS=flags['baseline'],
               GOMAXPROCS='4')
    revision = subprocess.check_output(['git', 'rev-parse', args.baseline], cwd=ROOT, text=True).strip()
    (out / 'candidate.patch').write_bytes(subprocess.check_output(['git', 'diff', 'HEAD', '--', 'go'], cwd=ROOT))
    sources = {}
    measurements = []
    with (out / 'commands.jsonl').open('w', buffering=1) as commands:
        def run(command, cwd, logfile):
            command = list(map(str, command))
            commands.write(json.dumps({'command': command, 'cwd': str(cwd), 'log': logfile, 'GOFLAGS': env['GOFLAGS']}) + '\n')
            with (out / logfile).open('w') as log:
                process = subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT)
            process.check_returncode()
            return (out / logfile).read_text()

        for label in ['baseline', 'candidate']:
            env['GOFLAGS'] = flags[label]
            source = work / label
            source.mkdir()
            if label == 'baseline':
                # Match the candidate's evidence exclusion: timing inputs are
                # source/default profiles, not every earlier experiment log.
                archive = subprocess.check_output(['git', 'archive', revision, 'go',
                                                   ':(exclude)go/bench/results'], cwd=ROOT)
                with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
                    tar.extractall(source, filter='data')
            else:
                shutil.copytree(ROOT / 'go', source / 'go', ignore=shutil.ignore_patterns('results', '__pycache__', '*.out', '*.test'))
            sources[label] = source
            # Freeze all sources, including any new assembly files missing from git diff.
            manifest = {str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest()
                        for p in sorted(source.rglob('*')) if p.is_file()}
            (out / f'{label}-sources.json').write_text(json.dumps(manifest, indent=2) + '\n')
            run(['go', 'build', '-o', source / 'concbench', './cmd/concbench'], source / 'go', f'{label}-build.log')
            env['GOFLAGS'] = table_flags[label]
            bench_build = ['go', 'test', '-c', '-o', source / 'bench.test']
            default_table_profile = source / 'go/bench/default.pgo'
            if '-pgo' not in table_flags[label] and default_table_profile.exists():
                bench_build.append('-pgo=' + str(default_table_profile))
            run(bench_build + ['.'], source / 'go/bench', f'{label}-bench-build.log')
        (out / 'setup.json').write_text(json.dumps({'baseline': revision, 'affinity': sorted(ALLOWED),
            'writer_cpu': 17, 'reader_cpus': '17-20', 'repeat': args.repeat,
            'benchtime': args.benchtime, 'table_GOMAXPROCS': args.table_procs, 'GOFLAGS': flags, 'table_GOFLAGS': table_flags, 'binaries': {label: hashlib.sha256((s / 'concbench').read_bytes()).hexdigest()
                for label, s in sources.items()}}, indent=2) + '\n')
        for case in ['fresh', 'latency', 'probe4', 'bench-uint64', 'bench-string']:
            if not re.search(args.cases, case):
                continue
            for repeat in range(args.repeat):
                for label in (['baseline', 'candidate'] if repeat % 2 == 0 else ['candidate', 'baseline']):
                    env['GOFLAGS'] = table_flags[label] if case.startswith('bench-') else flags[label]
                    source = sources[label]
                    if case in ['fresh', 'latency']:
                        command = ['taskset', '-c', '17', source / 'concbench', '-load-only', ROOT / 'words.txt', 1000000, 1]
                        if case == 'latency':
                            command.insert(5, '-load-latency')
                    elif case == 'probe4':
                        command = ['taskset', '-c', '17-20', source / 'concbench', '-load-workers=1', '-rounds=1', ROOT / 'words.txt', 1000000, 4]
                    else:
                        command = ['taskset', '-c', '17-30', 'make', 'bench', f'GOMAXPROCS={args.table_procs}', 'BENCH_CPU=17', 'BENCH_COUNT=1',
                                   f'BENCH_TIME={args.benchtime}', f'BENCH_KEY_TYPE={case[6:]}']
                    # Observe target CPUs and their SMT siblings before timing.
                    from recover_perf import cpu_busy, cpu_snapshot, cpu_delta
                    load = cpu_busy()
                    targets = [17, 18, 19, 20] if case == 'probe4' else [17]
                    observed = {f'cpu{i}': load[f'cpu{i}'] for i in targets + [i+24 for i in targets]}
                    commands.write(json.dumps({'before': case, 'repeat': repeat, 'label': label, 'cpu_busy_percent': observed}) + '\n')
                    if max(observed.values()) > 15:
                        raise RuntimeError(f'target or SMT sibling busy before {case}: {observed}')
                    logfile = f'{case}-{repeat}-{label}.log' 
                    before = cpu_snapshot()
                    output = run(command, source / 'go', logfile)
                    during = cpu_delta(before, cpu_snapshot())
                    siblings = {f'cpu{i+24}': during[f'cpu{i+24}'] for i in targets}
                    commands.write(json.dumps({'during': case, 'repeat': repeat, 'label': label,
                                               'sibling_busy_percent': siblings}) + '\n')
                    if max(siblings.values()) > 15:
                        raise RuntimeError(f'SMT sibling busy during {case}: {siblings}')
                    if case == 'fresh':
                        count, elapsed = map(int, re.search(r'count=(\d+) dt_ns=(\d+)', output).groups())
                        values = {'Mops/s': count * 1000 / elapsed}
                    elif case == 'latency':
                        values = {name: float(value) for name, value in re.findall(r'(p50|p95|p99|p999|max)=(\d+)', output.split('load latency ns')[1].splitlines()[0])}
                    elif case == 'probe4':
                        if 'misses=0' not in output:
                            raise RuntimeError('probe misses or incomplete output')
                        values = {'Mops/s': float(re.search(r'probe x4 ([\d.]+) mops', output)[1])}
                    else:
                        detailed = output.split('Detailed table (ns/key):')[1]
                        values = {cells[0]: float(cells[1]) for line in detailed.splitlines()
                                  if line.startswith('|') and (cells := [x.strip() for x in line.split('|')[1:-1]])
                                  and cells[0] not in ['Operation (showing ns/key)'] and not cells[0].startswith('-')}
                    measurements.append({'case': case, 'repeat': repeat, 'label': label, 'values': values, 'log': logfile, 'GOFLAGS': env['GOFLAGS']})
                    (out / 'measurements.json').write_text(json.dumps(measurements, indent=2) + '\n')
                    print(case, repeat, label, values, flush=True)
        lines = ['| Case / Wormhole operation | Baseline median | Candidate median | Throughput change |',
                 '| --- | ---: | ---: | ---: |']
        for case in ['fresh', 'latency', 'probe4', 'bench-uint64', 'bench-string']:
            samples = [m for m in measurements if m['case'] == case]
            if not samples:
                continue
            for operation in samples[0]['values']:
                a, b = [statistics.median(m['values'][operation] for m in samples if m['label'] == label)
                        for label in ['baseline', 'candidate']]
                gain = (b/a - 1) if case in ['fresh', 'probe4'] else (a/b - 1)
                lines.append(f'| {case} / {operation} | {a:.2f} | {b:.2f} | {gain:+.2%} |')
        summary = '\n'.join(lines) + '\n'
        (out / 'summary.md').write_text(summary)
        print(summary, flush=True)


if __name__ == '__main__':
    main()
