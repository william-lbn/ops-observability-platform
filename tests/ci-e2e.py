#!/usr/bin/env python3
"""Exercise the built application against real PostgreSQL, including raw evidence."""
import base64
import hashlib
import http.cookiejar
import json
import os
import pathlib
import secrets
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

root = pathlib.Path(__file__).resolve().parents[1]
dsn = os.environ['OPS_TEST_DATABASE_URL']
with tempfile.TemporaryDirectory() as directory:
    tmp = pathlib.Path(directory)
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-keyout', str(tmp/'key.pem'), '-out', str(tmp/'cert.pem'), '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'], check=True, capture_output=True)
    tokens = {role: secrets.token_hex(32) for role in ('admin', 'viewer', 'other')}
    config = {
        'databaseURL': dsn, 'listen': '127.0.0.1:18443', 'publicURL': 'https://localhost:18443',
        'tlsCert': str(tmp/'cert.pem'), 'tlsKey': str(tmp/'key.pem'),
        'principals': [{'subject': role, 'role': 'viewer' if role=='other' else role, 'tenant': 'other' if role=='other' else 'ci', 'tokenSHA256': hashlib.sha256(token.encode()).hexdigest()} for role,token in tokens.items()],
        'sources': [{'id':'reference-service','tenant':'ci','service':'reference-service','enabled':True}],
        'neonEnabled': False,
    }
    config_path = tmp/'config.json'; config_path.write_text(json.dumps(config))
    binary = str(root/'dist/platform')
    subprocess.run([binary, '-mode=migrate', '-config='+str(config_path)], check=True)
    log = (tmp/'app.log').open('w')
    process = subprocess.Popen([binary, '-config='+str(config_path), '-metrics-listen=127.0.0.1:19090'], stdout=log, stderr=subprocess.STDOUT)
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(tmp/'cert.pem'))), urllib.request.ProxyHandler({}))
    def request(path, role='admin', method='GET', body=None):
        req=urllib.request.Request('https://localhost:18443'+path, data=body, method=method, headers={'Authorization':'Bearer '+tokens[role], 'Content-Type':'application/json'})
        try:
            with opener.open(req,timeout=5) as response:return response.status,response.read(),response.headers
        except urllib.error.HTTPError as error:return error.code,error.read(),error.headers
    try:
        for attempt in range(40):
            try:
                if request('/readyz')[0]==200:break
            except OSError:pass
            time.sleep(0.25)
        else:raise RuntimeError('real application did not become ready')
        assert request('/api/v1/me','viewer')[0]==200
        assert request('/api/v1/incidents/ack','viewer','POST',b'{}')[0]==403
        assert json.loads(request('/api/v1/sources','other')[1])['items']==[]
        event_id=str(uuid.uuid4());now=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())
        event={'schemaVersion':'1.0','eventId':event_id,'sourceInstance':'reference-service','entityRef':{'sourceInstance':'reference-service','kind':'service','nativeId':'ci-reference','incarnation':None,'incarnationStatus':'unknown'},'eventKind':'service.observed','signalKind':'snapshot','occurredAt':None,'observedAt':now,'timeBasis':'source_observation','origin':{'providerId':'reference','providerVersion':'0.1.0','profileId':'reference.readonly','profileVersion':'1.0','mapperVersion':'1.0','transport':'poll','sampling':'unknown','nativeEventRef':None},'operationBinding':{'state':'none','operationRef':None,'bindingProofRef':None},'reliability':{'deliveryClass':'best_effort','evidenceProfileRef':None,'sourceCoverage':{'status':'partial','verifiedPrefix':None,'missingRanges':[],'proof':None}},'payload':{'observation':'real-ci-process'}}
        # The committed fixture defines the exact generic coverage shape.
        fixture=json.loads(root.joinpath('tests/event-template.json').read_text())
        fixture.update(eventId=event_id,observedAt=now,sourceInstance='reference-service')
        fixture['entityRef']['sourceInstance']='reference-service'
        fixture['entityRef']['nativeId']='ci-reference-process'
        fixture['origin'].update(providerId='reference',providerVersion='0.1.0',profileId='reference.readonly',mapperVersion='1.0',transport='poll',sampling='unknown')
        fixture['operationBinding']={'state':'none','operationRef':None,'bindingProofRef':None}
        fixture['reliability']['sourceCoverage'].update(sourceInstance='reference-service',asOf=now,coveredFrom=now,streamSetVersion='ci-reference-v1')
        fixture['payload']={'observation':'real-ci-process','processId':process.pid}
        fixture.pop('exampleMetadata',None)
        raw=json.dumps(fixture,ensure_ascii=False,indent=2).encode()
        status,data,_=request('/api/v1/events',method='POST',body=raw)
        assert status==200,(status,data)
        assert json.loads(request('/api/v1/events',method='POST',body=raw)[1])['duplicate'] is True
        assert request('/api/v1/events',method='POST',body=raw+b' ')[0]==409
        path='/api/v1/events/'+event_id+'?source=reference-service'
        status,download,headers=request(path,'viewer')
        assert status==200 and download==raw and headers['X-Content-SHA256']==hashlib.sha256(raw).hexdigest()
        assert request(path,'other')[0]==404
        exported=json.loads(request('/api/v1/export','viewer')[1]);assert exported['范围内存量统计']['来源事件']>=1 and any(row['事件标识']==event_id for row in exported['最近事件引用'])
        assert request('/api/v1/platform/dashboard','admin')[0]==403
        jar=http.cookiejar.CookieJar();browser=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(tmp/'cert.pem'))))
        def browser_request(endpoint,body=None):
            req=urllib.request.Request('https://localhost:18443/api/v1/'+endpoint,data=body,headers={'Content-Type':'application/json','Origin':'https://localhost:18443'})
            try:
                with browser.open(req,timeout=5) as res:return res.status,json.loads(res.read())
            except urllib.error.HTTPError as err:return err.code,json.loads(err.read())
        assert browser_request('login',json.dumps({'token':tokens['viewer']}).encode())[0]==200
        session=next(c.value for c in jar if c.name=='ops_session');assert session!=tokens['viewer'] and len(session)==64
        assert browser_request('me')[0]==200
        config['principals'][1]['disabled']=True;config_path.write_text(json.dumps(config));assert browser_request('me')[0]==401
        config['principals'][1]['disabled']=False;config_path.write_text(json.dumps(config));assert browser_request('me')[0]==200
        assert browser_request('logout',b'{}')[0]==200
        replay=urllib.request.Request('https://localhost:18443/api/v1/me',headers={'Cookie':'ops_session='+session})
        try:opener.open(replay,timeout=5);raise AssertionError('revoked session replay accepted')
        except urllib.error.HTTPError as err:assert err.code==401
        assert request('/api/v1/query?source=reference-service&signal=metrics&metric=up')[0]==422
        subprocess.run([binary,'-mode=prestop','-metrics-listen=127.0.0.1:19090'],check=True,timeout=12)
        assert request('/readyz')[0]==503 and request('/api/v1/events')[0]==200
        print('HTTPS, current authorization, tenant scope, viewer denial, real DB event commit, idempotency, conflict and byte-exact evidence download passed.')
    finally:
        process.terminate();process.wait(timeout=15);log.close()
