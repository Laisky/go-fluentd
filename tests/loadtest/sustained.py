#!/usr/bin/env python3
"""Audit steady CPU occupancy and throughput, separate from delivery correctness.

Use the same driver and concurrency for A/B. For profiling, use --bounded-fixtures
--delivery-window and enough requests for >= 20 s AFTER warmup. This is a bounded
closed-loop saturation workload, not an open-loop sustainable-rate certificate.
"""
from __future__ import annotations
import argparse
import bisect
import json
import math
from pathlib import Path
import statistics


def assess(samples: list[dict], requests: list[dict], epoch: int, begin: int,
           elapsed: float, capacity: float, trim: float = 3, minimum: float = .5,
           concurrency: int | None = None) -> dict:
    if not math.isfinite(capacity) or capacity <= 0:
        raise ValueError('invalid CPU capacity')
    points = sorted(samples, key=lambda s: s['at_ns'])
    times = [s['at_ns'] for s in points]
    assert len(times)>1 and all(b>a for a,b in zip(times,times[1:])), 'invalid observation times'
    assert all(math.isfinite(p['cpu_seconds']) for p in points), 'invalid CPU observation'
    assert all(b['cpu_seconds']>=a['cpu_seconds'] for a,b in zip(points,points[1:])), 'CPU counter decreased'
    def cpu(at):
        i=bisect.bisect_right(times,at)-1
        assert 0<=i<len(points)-1, 'CPU window outside observations'
        a,b=points[i:i+2]
        return a['cpu_seconds']+(b['cpu_seconds']-a['cpu_seconds'])*(at-a['at_ns'])/(b['at_ns']-a['at_ns'])
    events=[]; finishes=[]
    for r in requests:
        assert not r['Dropped'] and not r.get('error') and r['Status'] in (200,204), 'failed arrivals'
        assert r['Sink'] and all(t>=r['Start'] for t in r['Sink']), 'missing destination'
        done=max(r['Sink']);finishes.append(done)
        events.extend([(r['Start'],1),(done,-1)])
    pending=peak=0
    for _,delta in sorted(events, key=lambda e:(e[0],e[1])):
        pending+=delta;peak=max(peak,pending)
    finishes.sort()
    count=max(0,math.floor(elapsed-2*trim))
    rows=[]
    for i in range(count):
        start=begin+int((trim+i)*1e9);end=start+10**9
        used=cpu(epoch+end)-cpu(epoch+start)
        rows.append({'second':trim+i,'app_cpu_cores':used,'app_cpu_fraction':used/capacity,
            'delivered':bisect.bisect_left(finishes,end)-bisect.bisect_left(finishes,start)})
    occupancy=[r['app_cpu_fraction'] for r in rows];rates=[r['delivered'] for r in rows]
    mean=statistics.mean(occupancy) if rows else 0
    fraction=sum(x>=minimum for x in occupancy)/len(rows) if rows else 0
    cv=statistics.pstdev(rates)/statistics.mean(rates) if rates and statistics.mean(rates)>0 else math.inf
    qualified=len(rows)>=15 and mean>=minimum and fraction>=.8 and cv<=.2 and (concurrency is None or peak<=concurrency)
    return {'qualified':qualified,'cpu_capacity_cores':capacity,'minimum_cpu_fraction':minimum,
            'steady_seconds':len(rows),'mean_app_cpu_fraction':mean,'fraction_windows_above_minimum':fraction,
            'delivered_rate_cv':cv,'max_inflight_to_destination':peak,'windows':rows}


def audit(root:Path,minimum:float=.5)->dict:
    read=lambda n:json.loads((root/n).read_text())
    s=read('summary.json');o=read('options.json');m=read('measurement.json')
    assert s['passed'] and read('process.json')['graceful'], 'invalid trial'
    assert o.get('DeliveryWindow') and o.get('BoundedFixtures'), 'not a bounded end-to-end workload'
    quota,period=s['cpu.max'].split()
    if quota=='max':
        capacity=float(s['visible_cpus'])
    else:
        capacity=min(float(s['visible_cpus']),int(quota)/int(period))
    rs=read('requests.json')[o['Warmup']:]
    result=assess([m['before'],*read('resources.json'),m['after']],rs,s['epoch_unix_ns'],m['begin_ns'],m['elapsed_seconds'],capacity,minimum=minimum,concurrency=o['Concurrency'])
    if o['Profile']:
        p=read('profile-window.json')
        assert p['begin_ns']>=m['begin_ns']+int(3e9), 'profile started before steady warmup'
        assert p['end_ns']<=m['begin_ns']+int((m['elapsed_seconds']-1)*1e9), 'profile includes drain or idle tail'
        for name in ('cpu.pprof','heap-start.pprof','heap-end.pprof'):
            assert (root/name).read_bytes()[:2]==b'\x1f\x8b', 'missing pprof'
        result['profile_window']=p
    return result


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('artifacts',type=Path);p.add_argument('--minimum-cpu',type=float,default=.5)
    p.add_argument('--report-only',action='store_true',help='report a lower-CPU optimized candidate without claiming it meets the profiling gate')
    a=p.parse_args();r=audit(a.artifacts,a.minimum_cpu)
    (a.artifacts/'sustained.json').write_text(json.dumps(r,indent=2)+'\n')
    print(json.dumps({k:v for k,v in r.items() if k!='windows'},indent=2))
    if not r['qualified'] and not a.report_only:raise SystemExit('workload did not satisfy sustained profiling gate')
if __name__=='__main__':main()
