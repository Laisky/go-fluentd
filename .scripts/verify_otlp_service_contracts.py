#!/usr/bin/env python3
"""Require assertion failures for independently unsafe listener variants."""
import argparse,json,pathlib,shutil,subprocess,tempfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
CASES=[
 ('cleartext', 'if !ip.IsLoopback() && c.TLSCertFile == "" {', 'if false && !ip.IsLoopback() && c.TLSCertFile == "" {',
  'TestOTLPServiceInvalidConfigurationHasNoStorageEffects/public-cleartext','invalid service accepted'),
 ('authentication', 'BearerToken: token,', 'BearerToken: token[:0],',
  'TestOTLPServiceHTTPRejectionsAndRouteIsolation','wanted 401'),
 ('connections', 'netutil.LimitListener(ln, c.MaxConnections)', 'netutil.LimitListener(ln, c.MaxConnections+128)',
  'TestOTLPServiceConnectionAndReadDeadlines','connection limit bypassed'),
 ('headers', 'ReadHeaderTimeout: c.ReadHeaderTimeout,', 'ReadHeaderTimeout: time.Hour,',
  'TestOTLPServiceConnectionAndReadDeadlines','slow headers did not time out at server'),
]
CONTROL='TestOTLPServiceStartupFailureReleasesResources'

def run(root,name):
 p=subprocess.run(['go','test','-mod=readonly','-count=1','-timeout=30s','-json','-run','^'+name+'$','./internal/controller'],cwd=root,capture_output=True,text=True,timeout=60)
 rows=[json.loads(x) for x in p.stdout.splitlines() if x.startswith('{')]
 bad=('panic:', 'WARNING: DATA RACE','build failed','timed out')
 if any(x in p.stdout+p.stderr for x in bad) or any(x.get('Action')=='skip' for x in rows):raise AssertionError('invalid reproduction mechanism: '+p.stdout+p.stderr)
 return p,rows

def main():
 parser=argparse.ArgumentParser();parser.add_argument('--artifacts',type=pathlib.Path,required=True);args=parser.parse_args();args.artifacts.mkdir(parents=True,exist_ok=True)
 evidence=[]
 with tempfile.TemporaryDirectory(prefix='otlp-service-mutations-') as tmp:
  dst=pathlib.Path(tmp)/'repo';shutil.copytree(ROOT,dst,ignore=shutil.ignore_patterns('.git','__pycache__','.audit'))
  path=dst/'internal/controller/otlp_service.go';source=path.read_text()
  for label,before,after,test,diagnostic in CASES:
   assert source.count(before)==1,('mutation target drift',label)
   path.write_text(source);p,rows=run(dst,test);assert p.returncode==0 and any(r.get('Test')==test and r.get('Action')=='pass' for r in rows),('safe test failed',label,p.stdout,p.stderr)
   path.write_text(source.replace(before,after))
   p,rows=run(dst,test);assert p.returncode!=0 and any(r.get('Test')==test and r.get('Action')=='fail' for r in rows) and diagnostic in p.stdout,('mutant not caught by named assertion',label,p.stdout,p.stderr)
   (args.artifacts/(label+'.jsonl')).write_text(p.stdout);(args.artifacts/(label+'.stderr')).write_text(p.stderr)
   p,rows=run(dst,CONTROL);assert p.returncode==0 and any(r.get('Test')==CONTROL and r.get('Action')=='pass' for r in rows),('positive control failed',label,p.stdout,p.stderr)
   (args.artifacts/(label+'-control.jsonl')).write_text(p.stdout);evidence.append({'case':label,'safe_passed':True,'mutant_assertion_failed':True,'control_passed':True})
  path.write_text(source)
 (args.artifacts/'summary.json').write_text(json.dumps(evidence,indent=2));print(json.dumps(evidence,indent=2))
if __name__=='__main__':main()
