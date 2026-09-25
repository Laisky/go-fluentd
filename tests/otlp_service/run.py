#!/usr/bin/env python3
"""Exercise the ordinary configured executable; no imported application codecs.

Peers retain fsynced request/status ledgers. Protobuf fixtures are literal field
encodings, not produced by go-fluentd. This is not standalone Collector testing.
"""
from __future__ import annotations
import argparse, base64, gzip, hashlib, http.server, json, os, pathlib
import signal, socket, struct, subprocess, threading, time, urllib.request, urllib.error


def dump(path, value):
    with open(path, 'a', encoding='utf-8') as f:
        f.write(json.dumps(value, ensure_ascii=False, sort_keys=True)+'\n'); f.flush(); os.fsync(f.fileno())


def uvar(n):
    b=bytearray()
    while n>127: b.append((n&127)|128);n>>=7
    b.append(n);return bytes(b)


def field(n, b):
    if isinstance(b,str): b=b.encode()
    return uvar((n<<3)|2)+uvar(len(b))+b


def fixed(n, value):
    return uvar((n<<3)|1)+struct.pack('<Q', value)


def payload(sig, ct, seq):
    marker=f'event-{seq}-é-世界'
    ns=1780000000000000001+seq
    if ct=='application/json':
        if sig=='logs':
            name='resourceLogs';scope='scopeLogs';items='logRecords'
            rec={'timeUnixNano':str(ns),'body':{'stringValue':marker}}
        elif sig=='traces':
            name='resourceSpans';scope='scopeSpans';items='spans'
            rec={'traceId':'11'*16,'spanId':('22' if seq==1 else '33')*8,'name':marker,'startTimeUnixNano':str(ns),'endTimeUnixNano':str(ns+1)}
        else:
            name='resourceMetrics';scope='scopeMetrics';items='metrics'
            rec={'name':marker,'gauge':{'dataPoints':[{'timeUnixNano':str(ns),'asInt':'9223372036854775807'}]}}
        obj={name:[{scope:[{items:[rec]}]}],'futureEnvelope':{'sequence':seq,'value':'18446744073709551615'}}
        return json.dumps(obj,ensure_ascii=False,separators=(',',':')).encode()
    if sig=='logs': rec=fixed(1,ns)+field(5,field(1,marker))
    elif sig=='traces': rec=field(1,b'\x11'*16)+field(2,bytes([0x21+seq])*8)+field(5,marker)+fixed(7,ns)+fixed(8,ns+1)
    else: rec=field(1,marker)+field(5,field(1,fixed(3,ns)+fixed(6,9223372036854775807)))
    # Resource<signal> -> Scope<signal> -> one record/metric; unknown field 2047.
    return field(1,field(2,field(2,rec)))+field(2047,marker)


def partial(sig, ct):
    if ct=='application/json':
        key={'logs':'rejectedLogRecords','metrics':'rejectedDataPoints','traces':'rejectedSpans'}[sig]
        return json.dumps({'partialSuccess':{key:'1','errorMessage':'test terminal rejection'}}).encode()
    return field(1,b'\x08\x01'+field(2,'test terminal rejection'))


def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]


def request(url, body=None, ct='application/json', token='', compressed=False):
    headers={'Content-Type':ct}
    if token: headers['Authorization']='Bearer '+token
    if compressed:body=gzip.compress(body);headers['Content-Encoding']='gzip'
    req=urllib.request.Request(url,data=body,headers=headers)
    try:
        with urllib.request.urlopen(req,timeout=5) as r:return r.status,r.read()
    except urllib.error.HTTPError as e:return e.code,e.read()


def until(check, msg, timeout=10):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        try:
            if check():return
        except (OSError,ValueError,KeyError):pass
        time.sleep(.02)
    raise AssertionError(msg)


class Process:
    def __init__(self,binary,cfg,root,number,token):
        self.log=open(root/f'process-{number}.log','wb')
        env=os.environ.copy();env['TEST_EXEC_OTLP_TOKEN']=token
        self.p=subprocess.Popen([str(binary),'-c',str(cfg),'--env','sit','--addr',f'127.0.0.1:{cfg.management}','--log-level','error'],stdout=self.log,stderr=subprocess.STDOUT,env=env,cwd=root)
    def stop(self,kill=False):
        if self.p.poll() is None:self.p.send_signal(signal.SIGKILL if kill else signal.SIGTERM)
        try:self.p.wait(timeout=10)
        except subprocess.TimeoutExpired:self.p.kill();self.p.wait();raise AssertionError('shutdown did not terminate')
        self.log.close()
        if not kill and self.p.returncode!=0:raise AssertionError(f'graceful process exit {self.p.returncode}')


# A Path cannot carry metadata; keep path and management address together.
class ConfigPath:
    def __init__(self,path,management):self.path=path;self.management=management
    def __str__(self):return str(self.path)


def config(root,listen,management,peer,wal_gzip):
    obj={'settings':{
        'otlp':{'enabled':True,'listen_addr':f'127.0.0.1:{listen}','storage_dir':str(root/'state'),'bearer_token_env':'TEST_EXEC_OTLP_TOKEN','journal_gzip':wal_gzip,'replay_batch':8,'replay_interval':'50ms','max_wire_bytes':65536,'max_decoded_bytes':65536,'destinations':[
          {'id':d,**{s+'_endpoint':f'{peer}/{d}/v1/{s}' for s in ['logs','metrics','traces']},'max_attempts':1,'timeout':'2s','gzip':True} for d in ['a','b','c']]},
        'journal':{'buf_dir_path':str(root/'legacy'),'buf_file_bytes':1048576,'committed_id_sec':120,'gc_inteval_sec':3600},
    }}
    path=root/'settings.yml';path.write_text(json.dumps(obj,indent=2));return obj,ConfigPath(path,management)


def audit(root):
    manifest=[json.loads(x) for x in (root/'accepted.jsonl').read_text().splitlines()]
    ledger=[json.loads(x) for x in (root/'wire.jsonl').read_text().splitlines()]
    expected={hashlib.sha256(base64.b64decode(x['payload'])).hexdigest():base64.b64decode(x['payload']) for x in manifest}
    assert len(expected)==2 and all(x['status']==200 for x in manifest),'invalid source receipts'
    # Do not trust a jointly edited manifest and ledger: regenerate test payloads.
    meta=json.loads((root/'case.json').read_text())
    regenerated={hashlib.sha256(payload(meta['signal'],meta['content_type'],seq)).hexdigest():payload(meta['signal'],meta['content_type'],seq) for seq in [1,2]}
    assert expected==regenerated,'source manifest is not the caller workload'
    counts={}
    for row in ledger:
        raw=base64.b64decode(row['wire'])
        decoded=gzip.decompress(raw) if row['encoding']=='gzip' else raw
        h=hashlib.sha256(decoded).hexdigest()
        assert h in expected and decoded==expected[h],'unexpected or changed export'
        assert row['content_type']==meta['content_type'] and row['signal']==meta['signal'],'metadata changed'
        assert row['peer'] in ['a','b','c'],'unexpected destination'
        if row['status']==200:counts[(row['peer'],h)]=counts.get((row['peer'],h),0)+1
        else: assert row['peer']=='b' and row['status']==503,'unexpected result'
    assert all(counts.get((p,h))==1 for p in ['a','b','c'] for h in expected),'missing or repeated saved destination outcome'
    return {'accepted':2,'fully_accepted_destination_envelopes':4,'quarantined_destination_envelopes':2,'wire_attempts':len(ledger)}


def run_case(binary,root,sig,ct,wal_gzip):
    root.mkdir(parents=True);(root/'state').mkdir(mode=0o700)
    (root/'case.json').write_text(json.dumps({'signal':sig,'content_type':ct,'wal_gzip':wal_gzip}))
    gate=threading.Event();mu=threading.Lock();procs=[]
    class Peer(http.server.BaseHTTPRequestHandler):
        protocol_version='HTTP/1.1'
        def log_message(self,*args):pass
        def do_POST(self):
            body=self.rfile.read(int(self.headers.get('Content-Length','0')))
            parts=self.path.strip('/').split('/');dest=parts[0];signal_name=parts[-1]
            status=503 if dest=='b' and not gate.is_set() else 200
            response=partial(sig,ct) if dest=='c' else (b'{}' if ct=='application/json' else b'')
            with mu:dump(root/'wire.jsonl',{'peer':dest,'signal':signal_name,'content_type':self.headers.get('Content-Type'),'encoding':self.headers.get('Content-Encoding',''),'wire':base64.b64encode(body).decode(),'status':status})
            self.send_response(status);self.send_header('Content-Type',ct);self.send_header('Content-Length',str(len(response)));self.end_headers()
            try:self.wfile.write(response)
            except (BrokenPipeError,ConnectionResetError):pass
    peer=http.server.ThreadingHTTPServer(('127.0.0.1',0),Peer);thread=threading.Thread(target=peer.serve_forever,daemon=True);thread.start()
    listen,management=port(),port();token='local-fixture-only'
    obj,cfg=config(root,listen,management,f'http://127.0.0.1:{peer.server_port}',wal_gzip)
    base=f'http://127.0.0.1:{listen}';mgmt=f'http://127.0.0.1:{management}'
    def start(n):
        p=Process(binary,cfg,root,n,token);procs.append(p)
        until(lambda:request(mgmt+'/health')[0]==200,'configured binary not ready')
        assert p.p.poll() is None,'process exited during startup';return p
    def counters():return json.loads(request(mgmt+'/monitor')[1])['otlp']
    def receive(seq):
        raw=payload(sig,ct,seq);status,response=request(base+'/v1/'+sig,raw,ct,token,True)
        assert status==200 and response==(b'{}' if ct=='application/json' else b''),'not an OTLP full-acceptance response'
        dump(root/'accepted.jsonl',{'sequence':seq,'payload':base64.b64encode(raw).decode(),'status':status,'response':base64.b64encode(response).decode()})
    try:
        p=start(1)
        # Ordinary management routes are not exposed on the dedicated listener.
        assert request(base+'/monitor')[0]==404
        assert request(base+'/v1/'+sig,b'{}','application/json','wrong')[0]==401
        assert request(base+'/v1/'+sig,b'{','application/json',token)[0]==400
        assert request(base+'/v1/'+sig,b'x'*65537,'application/json',token)[0]==413
        receive(1)
        until(lambda:counters()['acceptedEnvelopes']==1 and counters()['quarantinedEnvelopes']==1,'mixed peer receipts were not persisted')
        generation=(root/'state/generation.json').read_bytes();p.stop(kill=True)
        gate.set();p=start(2)
        until(lambda:counters()['acceptedEnvelopes']==1,'retryable destination not delivered after restart')
        assert (root/'state/generation.json').read_bytes()==generation,'namespace changed'
        p.stop(kill=True);p=start(3);receive(2)
        until(lambda:counters()['acceptedEnvelopes']==2 and counters()['quarantinedEnvelopes']==1,'new record did not progress after second restart')
        assert (root/'state/generation.json').read_bytes()==generation,'namespace changed'
        p.stop();procs.remove(p)
        result=audit(root)
        # Negative evidence control: changing both copies is not delivery proof.
        original=(root/'accepted.jsonl').read_bytes();rows=[json.loads(x) for x in original.splitlines()];rows[0]['payload']=base64.b64encode(b'forged').decode();(root/'accepted.jsonl').write_text('\n'.join(json.dumps(x) for x in rows)+'\n')
        try:
            try:audit(root)
            except AssertionError:pass
            else:raise AssertionError('audit accepted corrupted manifest')
        finally:(root/'accepted.jsonl').write_bytes(original)
        (root/'audit.json').write_text(json.dumps(result,indent=2));return result
    finally:
        for p in procs:
            if p.p.poll() is None:p.stop(kill=True)
        peer.shutdown();peer.server_close();thread.join(timeout=3)


def failure_controls(binary,root):
    root.mkdir(parents=True);(root/'state').mkdir(mode=0o700)
    listen,management=port(),port();obj,cfg=config(root,listen,management,'http://127.0.0.1:1',False)
    env=os.environ.copy();env['TEST_EXEC_OTLP_TOKEN']='test'
    for name,change in [
       ('overlapping-storage',lambda o:o['settings']['journal'].update({'buf_dir_path':str(root/'state')})),
       ('dry',lambda o:None),
       ('unknown-key',lambda o:o['settings']['otlp'].update({'misspelled_setting':True})),
       ('missing-storage',lambda o:o['settings']['otlp'].update({'storage_dir':str(root/'missing')})),
       ('public-cleartext',lambda o:o['settings']['otlp'].update({'listen_addr':'0.0.0.0:0'})),
       ('missing-token',lambda o:o['settings']['otlp'].update({'bearer_token_env':'NOT_SET_OTLP_TOKEN'})),
    ]:
        copy=json.loads(json.dumps(obj));change(copy);path=root/(name+'.yml');path.write_text(json.dumps(copy))
        p=subprocess.run([str(binary),'-c',str(path),'--env','sit','--addr',f'127.0.0.1:{management}','--log-level','error']+(['--dry'] if name=='dry' else []),cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=10)
        (root/(name+'.log')).write_bytes(p.stdout);assert p.returncode!=0,f'{name} failed to return nonzero'
    # Disabled integration leaves the existing management/legacy paths available.
    obj['settings']['otlp']={'enabled':False};cfg.path.write_text(json.dumps(obj));p=Process(binary,cfg,root,'disabled','test')
    try:until(lambda:request(f'http://127.0.0.1:{management}/health')[0]==200,'disabled mode broke legacy startup');p.stop()
    finally:
        if p.p.poll() is None:p.stop(kill=True)


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--binary',type=pathlib.Path,required=True);parser.add_argument('--artifacts',type=pathlib.Path,required=True);args=parser.parse_args()
    args.binary=args.binary.resolve();args.artifacts=args.artifacts.resolve();args.artifacts.mkdir(parents=True,exist_ok=True)
    summary={'binary_sha256':hashlib.sha256(args.binary.read_bytes()).hexdigest(),'results':[],'passed':False}
    try:
        failure_controls(args.binary,args.artifacts/'controls')
        for sig in ['logs','metrics','traces']:
            for ct in ['application/json','application/x-protobuf']:
                for gz in [False,True]:
                    name=f'{sig}-{ct.split("/")[-1]}-wal-{int(gz)}'
                    result=run_case(args.binary,args.artifacts/name,sig,ct,gz);summary['results'].append({'case':name,**result});print(name,'PASS',flush=True)
        summary['passed']=True
    finally:(args.artifacts/'summary.json').write_text(json.dumps(summary,indent=2))

if __name__=='__main__':main()
