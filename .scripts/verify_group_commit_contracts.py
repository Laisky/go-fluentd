#!/usr/bin/env python3
"""Verify receipt tests reject three deliberately unsafe barriers in isolation.

This is a test-quality control, not a production feature or a timing benchmark.
The original checkout is never modified. Compilation failures, unexpected test
failures, panics and process timeouts are not accepted as reproductions.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

CONTROL='TestJournalWriterBestEffortDoesNotRequestSync'
RECEIPT='TestJournalWriterReceiptsFollowSuccessfulBarrier'
GROUP='TestJournalGroupSharedFailureAndSubsequentSuccess'


def run(worktree, tests, log):
    pattern='^('+ '|'.join(tests)+')$'
    proc=subprocess.run(['go','test','-mod=readonly','-json','-count=1','-timeout=60s','-run',pattern,'./internal/controller'],cwd=worktree,capture_output=True,text=True,timeout=90)
    log.write_text(proc.stdout+proc.stderr)
    if proc.returncode not in (0,1) or '[build failed]' in proc.stdout+proc.stderr or 'panic:' in proc.stdout+proc.stderr:
        raise AssertionError(f'fixture/process failure, not a contract reproduction: {log}')
    events=[json.loads(line) for line in proc.stdout.splitlines() if line.startswith('{')]
    failed={e['Test'] for e in events if e.get('Action')=='fail' and 'Test' in e}
    passed={e['Test'] for e in events if e.get('Action')=='pass' and 'Test' in e}
    if any(e.get('Action')=='skip' for e in events):
        raise AssertionError('skips are not permitted')
    return proc.returncode,failed,passed


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--evidence',type=Path,required=True);args=p.parse_args()
    args.evidence.mkdir(parents=True,exist_ok=False)
    repo=Path(subprocess.check_output(['git','rev-parse','--show-toplevel'],text=True).strip())
    revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip()
    with tempfile.TemporaryDirectory(prefix='group-contract-') as parent:
        worktree=Path(parent)/'source'
        subprocess.run(['git','worktree','add','--detach',str(worktree),revision],cwd=repo,check=True,stdout=subprocess.DEVNULL)
        try:
            path=worktree/'internal/controller/journal_writer.go';original=path.read_text()
            marker='return writer.Sync()'
            if original.count(marker)!=1:raise AssertionError('mutation point changed; update the test explicitly')
            code,failed,passed=run(worktree,[RECEIPT,GROUP,CONTROL],args.evidence/'correct.jsonl')
            assert code==0 and not failed and {RECEIPT,GROUP,CONTROL}<=passed,(code,failed,passed)
            cases=[
                ('skip_sync','return nil',RECEIPT,{RECEIPT}),
                ('early_success','for _, msg := range batch { msg.CompleteAcceptance(nil) }; return writer.Sync()',RECEIPT,{RECEIPT}),
                ('ignore_sync_error','_ = writer.Sync(); return nil',GROUP,{GROUP,GROUP+'/sync'}),
            ]
            results=[]
            for name,replacement,target,expected in cases:
                path.write_text(original.replace(marker,replacement))
                code,failed,passed=run(worktree,[target,CONTROL],args.evidence/f'{name}.jsonl')
                assert code==1 and failed==expected and CONTROL in passed,(name,code,failed,passed)
                results.append(dict(mutation=name,expected_failed=sorted(expected),actual_failed=sorted(failed),positive_control=CONTROL))
            (args.evidence/'summary.json').write_text(json.dumps({'revision':revision,'source_sha256':hashlib.sha256(original.encode()).hexdigest(),'correct_passed':True,'mutations':results},indent=2))
            print(json.dumps(results,indent=2))
        finally:
            subprocess.run(['git','worktree','remove','--force',str(worktree)],cwd=repo,check=True)


if __name__=='__main__':main()
