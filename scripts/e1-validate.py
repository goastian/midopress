#!/usr/bin/env python3
"""Frozen HAR cold/warm baseline and local protocol fixtures; never touches live services."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]

def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def capture(command):
    result = subprocess.run(command, cwd=ROOT, text=True, capture_output=True)
    return {"exit_code": result.returncode, "stdout": result.stdout, "stderr": result.stderr}

def hardware():
    model = "unknown"
    if Path('/proc/cpuinfo').exists():
        model = next((line.split(':', 1)[1].strip() for line in Path('/proc/cpuinfo').read_text().splitlines() if line.startswith('model name')), model)
    mem = {}
    if Path('/proc/meminfo').exists():
        mem = {line.split(':')[0]: line.split(':')[1].strip() for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith(('MemTotal:', 'SwapTotal:'))}
    limits = {}
    for name in ('cpu.max', 'memory.max', 'cpuset.cpus.effective'):
        path = Path('/sys/fs/cgroup') / name
        if path.exists(): limits[name] = path.read_text().strip()
    return {"os": platform.system(), "kernel": platform.release(), "arch": platform.machine(), "cpu_model": model,
            "logical_cpus": os.cpu_count(), "memory": mem, "cgroup": limits, "load_average_before": os.getloadavg() if hasattr(os, 'getloadavg') else None,
            "warning": "existing user proxy/traffic remains running; host contention is observed, not eliminated"}

def run_test(binary, name, pattern, out, profile=None):
    folder = out / name
    folder.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, 'MIDOPRESS_E1': '1', 'MIDOPRESS_E1_REPORT_DIR': str(folder)}
    if profile: env['MIDOPRESS_E1_PROFILE'] = profile
    command = [str(binary), '-test.run', pattern, '-test.v', '-test.timeout=15m']
    timer = Path('/usr/bin/time')
    if timer.exists(): command = [str(timer), '-v', '-o', str(folder / 'resources.txt'), *command]
    started = time.monotonic()
    print(f'E1 {name}: isolated fixtures, no live-service mutation', flush=True)
    with (folder / 'test.log').open('w') as log:
        child = subprocess.Popen(command, cwd=ROOT / 'internal/server', env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try: code = child.wait()
        except KeyboardInterrupt:
            os.killpg(child.pid, signal.SIGTERM)
            child.wait(timeout=10)
            raise
    return {"name": name, "exit_code": code, "seconds": time.monotonic()-started, "command": command,
            "log": str(folder / 'test.log'), "resources": str(folder / 'resources.txt')}

def report(out, metadata, executions):
    rows, compatibility = [], None
    for execution in executions:
        folder = out / execution['name']
        if (folder / 'corpus.json').exists():
            corpus = json.loads((folder / 'corpus.json').read_text())
            for run in (corpus.get('runs') or []):
                for phase in ('cold', 'warm'):
                    data = run[phase]
                    rows.append({"profile": run['profile'], "phase": phase, "responses": len(data['rows']),
                                 "origin_bytes": data['OriginBytes'] if 'OriginBytes' in data else data['origin_bytes'],
                                 "client_bytes": data['client_bytes'], "saved": data['saved'], "cache_hits": data['cache_hits'],
                                 "p50_ms": data['p50_ms'], "p95_ms": data['p95_ms'], "reasons": data['reasons']})
        if (folder / 'compatibility.json').exists(): compatibility = json.loads((folder / 'compatibility.json').read_text())
    expected = metadata.get('expected_profiles', ['balanced','saver','extreme','lossless'])
    sri_path = out/'sri/results.json'
    sri = json.loads(sri_path.read_text()) if sri_path.exists() else None
    go_complete = (len(executions)==len(expected)+1 and all(e['exit_code']==0 for e in executions)
                   and len(rows)==len(expected)*2 and all(r['responses']==metadata.get('corpus_entries',37) for r in rows)
                   and compatibility is not None and len(compatibility.get('cases') or [])>=7)
    payload = {"schema": 1, "metadata": metadata, "executions": executions, "corpus": rows, "compatibility": compatibility,
               "go_complete": go_complete, "sri_complete": bool(sri and sri.get('complete')), "browser_sri": sri,
               "complete": go_complete and bool(sri and sri.get('complete')),
               "limits": ["Local replay body transfer and HTTP timings; no browser LCP or Internet download savings.",
                          "Cold means new process/profile and empty conversion cache; OS page cache is not purged.",
                          "Warm follows a quiet background queue; privacy policy can intentionally prevent cache hits.",
                          "resources.txt records run CPU and peak RSS; RSS is not a simultaneous sum of every worker.",
                          "Dynamic browser SRI is a separate scenario; a Go protocol result alone does not establish it."]}
    (out/'summary.json').write_text(json.dumps(payload,indent=2)+'\n')
    lines = ['# E1 fixed-corpus and compatibility evidence', '', f'Corpus SHA256: `{metadata["corpus_sha256"]}`. Binary SHA256: `{metadata["binary_sha256"]}`.', '',
             f'Hardware: {metadata["hardware"]["cpu_model"]}; {metadata["hardware"]["logical_cpus"]} logical CPUs; {metadata["hardware"]["memory"]}.', '',
             '| Profile | Phase | Responses | Origin B | Client B | Credited saving | Cache hits | p50/p95 ms |', '|---|---|---:|---:|---:|---:|---:|---:|']
    for row in rows:
        percent=100*row['saved']/row['origin_bytes'] if row['origin_bytes'] else 0
        lines.append(f'| {row["profile"]} | {row["phase"]} | {row["responses"]} | {row["origin_bytes"]} | {row["client_bytes"]} | {percent:.2f}% | {row["cache_hits"]:.0f} | {row["p50_ms"]:.2f}/{row["p95_ms"]:.2f} |')
    lines += ['', '## Protocol matrix', '', '| Case | Observation |', '|---|---|']
    if compatibility:
        for case in (compatibility.get('cases') or []): lines.append(f'| {case["case"]} | {case["fallback"]} |')
    lines += ['', f'Go validation complete: **{go_complete}**. Browser SRI complete: **{payload["sri_complete"]}**.', '', '## Limits', '', *['- '+item for item in payload['limits']], '', 'Dynamic SRI: run `web/scenarios/e1-compatibility.mjs` against a separately started disposable proxy.']
    (out/'summary.md').write_text('\n'.join(lines)+'\n')
    return payload

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default=shutil.which('go'))
    parser.add_argument('--out', type=Path, default=ROOT/'artifacts/e1')
    parser.add_argument('--profiles', default='balanced,saver,extreme,lossless')
    parser.add_argument('--compatibility-only', action='store_true')
    parser.add_argument('--summarize-only', action='store_true')
    args=parser.parse_args();out=args.out.resolve();out.mkdir(parents=True,exist_ok=True)
    if args.summarize_only:
        metadata=json.loads((out/'metadata.json').read_text());executions=json.loads((out/'executions.json').read_text());report(out,metadata,executions);return
    if not args.go: parser.error('Go is required; use --go /path/to/go')
    binary=out/'server.test'
    build=capture([args.go,'test','-c','-o',str(binary),'./internal/server'])
    (out/'build.json').write_text(json.dumps(build,indent=2)+'\n')
    if build['exit_code']: print(build['stderr'],file=sys.stderr);sys.exit(build['exit_code'])
    metadata={"generated_utc":time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),"hardware":hardware(),
              "corpus_sha256":digest(ROOT/'testdata/corpus/corpus.har'),"corpus_entries":len(json.loads((ROOT/'testdata/corpus/corpus.har').read_text())['log']['entries']),
              "binary_sha256":digest(binary),"binary_buildinfo":capture([args.go,'version','-m',str(binary)]),
              "go_version":capture([args.go,'version']),"go_env":capture([args.go,'env','GOOS','GOARCH','CGO_ENABLED','GOAMD64']),
              "modules":capture([args.go,'list','-m','-json','all']),"go_sum_sha256":digest(ROOT/'go.sum'),
              "git_head":capture(['git','rev-parse','HEAD'])['stdout'].strip(),"tracked_diff_sha256":hashlib.sha256(subprocess.check_output(['git','diff','--binary'],cwd=ROOT)).hexdigest(),
              "expected_profiles":[] if args.compatibility_only else args.profiles.split(','),
              "gomaxprocs_env":os.environ.get('GOMAXPROCS'),"native_library_path":os.environ.get('LD_LIBRARY_PATH',''),
              "web_lock_sha256":digest(ROOT/'web/package-lock.json')}
    (out/'metadata.json').write_text(json.dumps(metadata,indent=2)+'\n')
    executions=[]
    try:
        if not args.compatibility_only:
            for profile in args.profiles.split(','):
                if profile not in ('balanced','saver','extreme','lossless'): parser.error('unknown profile '+profile)
                executions.append(run_test(binary,profile,'^TestE1FixedCorpusColdWarm$',out,profile))
                (out/'executions.json').write_text(json.dumps(executions,indent=2)+'\n')
        executions.append(run_test(binary,'compatibility','^TestE1CompatibilityMatrix$',out))
    finally:
        (out/'executions.json').write_text(json.dumps(executions,indent=2)+'\n')
        payload=report(out,metadata,executions)
    sys.exit(0 if payload['go_complete'] else 1)

if __name__=='__main__': main()
