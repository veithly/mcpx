#!/usr/bin/env python3
"""Probe a built macOS MCPX server in an isolated home without touching launchd."""
import argparse, json, os, pathlib, socket, subprocess, tempfile, time, urllib.request
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--binary', required=True, help='Signed MCPX candidate binary')
parser.add_argument('--output-dir', required=True, help='Directory for probe server logs')
args = parser.parse_args()
binary = str(pathlib.Path(args.binary).resolve())
output = pathlib.Path(args.output_dir).resolve()
output.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix='mcpx-probe-', dir='/tmp') as home:
    root = pathlib.Path(home)
    project = root / 'project'
    project.mkdir()
    (project / 'demo.txt').write_bytes(b'first\r\nold\r\n')
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    (root / 'config.yaml').write_text('auth:\n  mode: open\nsecurity:\n  commands:\n    default: allow\n    deny: []\nworkspaces:\n  - name: probe\n    path: ' + str(project) + '\nlogging:\n  enabled: false\ndiscovery:\n  skills:\n    dirs: [' + str(root / 'skills') + ']\n')
    env = dict(os.environ, MCPX_HOME=home, PATH='/usr/bin:/bin:/usr/sbin:/sbin')
    log = open(output / 'probe-server.log', 'w')
    proc = subprocess.Popen([binary, '-addr', '127.0.0.1:'+str(port)], cwd=project, env=env, stdout=log, stderr=log)
    counter = 0
    def rpc(method, params):
        global counter
        counter += 1
        body = {'jsonrpc':'2.0','id':counter,'method':method,'params':params}
        req = urllib.request.Request('http://127.0.0.1:'+str(port)+'/mcp', data=json.dumps(body).encode(), headers={'Content-Type':'application/json','Accept':'application/json, text/event-stream'})
        raw = urllib.request.urlopen(req, timeout=35).read().decode()
        result = json.loads(next((line[6:] for line in raw.splitlines() if line.startswith('data: ')), raw))
        assert 'error' not in result, result.get('error')
        return result['result']
    def call(tool, args, expected='succeeded'):
        result = rpc('tools/call', {'name':tool,'arguments':args})
        wire = result['structuredContent']
        assert wire['status'] == expected, (tool,wire.get('error'))
        return wire
    try:
        for _ in range(60):
            if proc.poll() is not None: raise RuntimeError('probe server exited; inspect probe-server.log')
            try:
                rpc('initialize', {'protocolVersion':'2025-03-26','capabilities':{},'clientInfo':{'name':'reliability-probe','version':'1'}})
                break
            except (OSError, urllib.error.URLError): time.sleep(.1)
        else: raise RuntimeError('probe server did not listen')
        catalog = rpc('tools/list', {})['tools']
        edit = next(t for t in catalog if t['name']=='edit')
        fields = edit['inputSchema']['properties']['edits']['items']['properties']
        assert 'rev' in fields and 'base_sha256' not in fields
        opened = call('session', {'workspace':'probe','label':'isolated reliability verification'})['data']
        sid = opened.get('remote_session_id') or opened.get('session_id')
        assert sid, list(opened)
        read = call('read', {'remote_session_id':sid,'path':'demo.txt','mode':'full'})['data']
        args = {'remote_session_id':sid,'purpose':'verify CRLF round trip and idempotency','idempotency_key':'probe-edit','edits':[{'path':'demo.txt','operation':'update','rev':read['rev'],'replacements':[{'match':'first\r\nold\r\n','replacement':'first\r\nnew\r\n'}]}]}
        call('edit', args); call('edit', args)
        assert (project/'demo.txt').read_bytes()==b'first\r\nnew\r\n'
        stale = call('edit', {'remote_session_id':sid,'purpose':'reject outdated connector schema','edits':[{'path':'demo.txt','operation':'update','base_sha256':'stale','content':'wrong'}]}, 'failed')
        assert stale['error']['details']['schema_source']=='tools/list'
        node = call('execute', {'remote_session_id':sid,'action':'run','purpose':'verify child process PATH','runtime':'node','script':"const {spawnSync}=require('child_process'); const r=spawnSync('node',['-e',\"process.stdout.write('child-ok')\"],{encoding:'utf8'}); process.stdout.write(r.stdout||''); process.exit(r.status??1);"})['data']
        assert node['stdout']=='child-ok' and node['exit_code']==0
        pending = call('execute', {'remote_session_id':sid,'action':'run','purpose':'verify async terminal result','execution_mode':'async','yield_time_ms':1,'command':'sleep 0.1; printf async-ok'}, 'accepted')['data']
        oid = pending['operation_id']
        finished = call('operation_manage', {'remote_session_id':sid,'action':'wait','operation_id':oid,'timeout_ms':5000})['data']
        assert finished['state']=='succeeded'
        cancelled = call('operation_manage', {'remote_session_id':sid,'action':'cancel','operation_id':oid})['data']
        assert cancelled['state']=='succeeded'
        print(json.dumps({'result':'passed','checks':['signed binary startup','published rev schema','read/edit CRLF bytes','idempotent replay','stale schema rejection with revision','Node child under daemon PATH','async wait','late cancellation preserves result'],'binary':binary},ensure_ascii=False))
    finally:
        proc.terminate()
        try: proc.wait(timeout=45)
        except subprocess.TimeoutExpired: proc.kill(); proc.wait()
        log.close()
