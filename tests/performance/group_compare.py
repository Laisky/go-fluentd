#!/usr/bin/env python3
"""Paired real-journal and executable group-commit measurements.

No timing threshold manufactures a passing optimization. Every sample must pass
its delivery/receipt contract. Report all samples, including slower profiles.
Pipeline latency is durable-acceptance latency, not an open-loop delivery SLO.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import subprocess

from pipeline import sample

SERVICE = re.compile(r'^BenchmarkJournalDurableService/(gzip=(?:false|true)/clients=(?:1|16|64))-\d+\s+(\d+)\s+(.*)$')
PROFILES = {f'gzip={g}/clients={c}' for g in ('false', 'true') for c in (1,16,64)}


def read_service(text, count):
    result = {}
    for line in text.splitlines():
        match = SERVICE.match(line)
        if not match:
            continue
        name, operations, tail = match.groups()
        if name in result or int(operations) != count:
            raise ValueError('duplicate workload or changed operation budget')
        parts = tail.split()
        if len(parts) % 2:
            raise ValueError('malformed benchmark metrics')
        metrics = {}
        for i in range(0,len(parts),2):
            val = float(parts[i]); unit = parts[i+1]
            if not math.isfinite(val) or val < 0 or unit in metrics:
                raise ValueError('invalid/duplicate benchmark metric')
            metrics[unit] = val
        if not {'ns/op','B/op','allocs/op','syncs/msg','msgs/op','msgs/s'} <= metrics.keys():
            raise ValueError('missing journal performance/durability metric')
        if metrics['msgs/op'] != 1 or not 0 < metrics['syncs/msg'] <= 1 or metrics['ns/op'] <= 0:
            raise ValueError('invalid work unit or missing durable barrier')
        result[name] = metrics
    if set(result) != PROFILES or not re.search(r'^PASS\s*$',text,re.M):
        raise ValueError('incomplete or failed journal measurement')
    return result


def stats(values):
    if not values or any(not math.isfinite(x) for x in values):
        raise ValueError('empty/non-finite measurement')
    return dict(median=statistics.median(values), min=min(values), max=max(values), samples=values)


def summarize(rows, kind, repeats):
    names = sorted({r['profile'] for r in rows})
    expected_names = PROFILES
    if set(names) != expected_names:
        raise ValueError(f'incomplete {kind} profiles')
    answer = {}
    metrics = ('ns/op','syncs/msg','B/op','allocs/op') if kind == 'service' else ('delivered_per_second','accepted_per_second','p99_ms','app_cpu_seconds','app_peak_rss_kib')
    for name in names:
        groups = {v: sorted((r for r in rows if r['profile']==name and r['variant']==v),key=lambda r:r['repeat']) for v in ('before','after')}
        for v, group in groups.items():
            if [r['repeat'] for r in group] != list(range(repeats)):
                raise ValueError(f'incomplete/duplicate pairs for {name}/{v}')
        item = {}
        for metric in metrics:
            b = [r['metrics'][metric] for r in groups['before']]
            a = [r['metrics'][metric] for r in groups['after']]
            item[metric] = {'before':stats(b),'after':stats(a)}
            if all(b):
                ratios = [y/x for x,y in zip(b,a)]
                item[metric]['paired_ratio'] = stats(ratios)
                item[metric]['median_change_percent'] = (statistics.median(a)/statistics.median(b)-1)*100
        answer[name] = item
    return answer


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for flag in ('before','after','before-service','after-service','output'):
        p.add_argument('--'+flag,type=Path,required=True)
    p.add_argument('--repeats',type=int,default=6)
    p.add_argument('--count',type=int,default=2048)
    p.add_argument('--mode',choices=('all','service','pipeline'),default='all')
    args = p.parse_args()
    if args.count<64 or args.count%64 or args.count>262144 or args.repeats<3:
        p.error('count must be a bounded multiple of 64; at least 3 pairs required')
    args.output.mkdir(parents=True,exist_ok=False)
    files = {v:getattr(args,v.replace('-','_')).resolve(strict=True) for v in ('before','after','before-service','after-service')}
    metadata = {'binaries':{v:{'path':str(f),'sha256':hashlib.sha256(f.read_bytes()).hexdigest()} for v,f in files.items()},'count':args.count,'repeats':args.repeats,'GOMAXPROCS':os.environ.get('GOMAXPROCS'),'policy':'before per-record; after default bounded ready-only grouping; same success-after-Sync contract; no batching timer'}
    (args.output/'manifest.json').write_text(json.dumps(metadata,indent=2))
    service_rows, pipeline_rows = [],[]
    for n in range(args.repeats):
        order=('before','after') if n%2==0 else ('after','before')
        if args.mode in ('all','service'):
            for variant in order:
                proc=subprocess.run([str(files[variant+'-service']),'-test.run=^$','-test.bench=^BenchmarkJournalDurableService',f'-test.benchtime={args.count}x','-test.count=1'],capture_output=True,text=True,timeout=240)
                text=proc.stdout+proc.stderr
                (args.output/f'service-{n}-{variant}.txt').write_text(text)
                if proc.returncode:
                    raise RuntimeError(f'{variant} service failed: {proc.returncode}: {text}')
                values=read_service(text,args.count)
                if variant=='before' and any(v['syncs/msg']!=1 for v in values.values()):
                    raise ValueError('baseline must use one Sync per record')
                for profile,metrics in values.items():
                    service_rows.append(dict(repeat=n,variant=variant,profile=profile,metrics=metrics))
                (args.output/'service-samples.json').write_text(json.dumps(service_rows,indent=2))
        if args.mode in ('all','pipeline'):
            for gz in (False,True):
                for clients in (1,16,64):
                    profile=f'gzip={str(gz).lower()}/clients={clients}'
                    for variant in order:
                        r=sample(files[variant],args.output/f'pipeline-{n}-{gz}-{clients}-{variant}',args.count,clients,gz)
                        if r['accepted']!=args.count or r['delivered_per_sink']!=[args.count,args.count] or not r['durable_ack']:
                            raise ValueError('pipeline delivery contract failed')
                        m={k:r[k] for k in ('delivered_per_second','accepted_per_second','app_cpu_seconds','app_peak_rss_kib')}
                        m['p99_ms']=r['latency_ms']['p99']
                        pipeline_rows.append(dict(repeat=n,variant=variant,profile=profile,metrics=m,details=r))
                        (args.output/'pipeline-samples.json').write_text(json.dumps(pipeline_rows,indent=2))
                        print(n,variant,profile,json.dumps(m),flush=True)
    summary={'manifest':metadata,'method':'same-host counterbalanced pairs; medians, full ranges and paired ratios; no statistical significance claim'}
    if service_rows:summary['service']=summarize(service_rows,'service',args.repeats)
    if pipeline_rows:summary['pipeline']=summarize(pipeline_rows,'pipeline',args.repeats)
    (args.output/'summary.json').write_text(json.dumps(summary,indent=2))
    print(json.dumps(summary,indent=2),flush=True)


if __name__=='__main__':
    main()
