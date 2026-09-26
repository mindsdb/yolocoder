"""Real YoloCoder/process/browser integration checks; no paid model calls."""
import argparse
import json
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from yoloeval.catalog import get_task
from yoloeval.paired import run_paired
from yoloeval.runner import execute,write_json
from yoloeval.runtime import Runtime


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--binary',type=Path,default=Path.home()/'.local/bin/yolocoder')
    args=parser.parse_args();out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    instances=[]
    class ObservedRuntime(Runtime):
        def __init__(self,*args,**kwargs):super().__init__(*args,**kwargs);instances.append(self)
    class SlowProvider(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def do_POST(self):
            self.rfile.read(int(self.headers['Content-Length']));time.sleep(2)
            body=json.dumps({'model':'offline-control','output':[],'usage':{'input_tokens':1,'output_tokens':1}}).encode()
            try:
                self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            except (BrokenPipeError,ConnectionResetError):pass
    server=ThreadingHTTPServer(('127.0.0.1',0),SlowProvider)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    provider={'base_url':f'http://127.0.0.1:{server.server_port}','api_key':'local-test-only','model':'offline-control','api':'responses'}
    try:
        with patch('yoloeval.runner.Runtime',ObservedRuntime):
            timeout=execute(get_task('notes.bug'),0,out/'timeout',args.binary,provider,timeout=.5,control='solution')
            deadline=json.loads((out/'timeout/deadline.json').read_text())
            timeout_ok=timeout['status']=='completed' and not timeout['passed'] and timeout.get('agent_error')=='Task exceeded 0.5s' and deadline['agent_wall_s_at_deadline']>=.5
            config=SimpleNamespace(output=out/'paired',split='holdout',tasks='notes.edit',repeats=1,seed=42,timeout=180,prices=None,
                baseline_binary=args.binary,candidate_binary=args.binary,baseline_model=None,candidate_model=None,baseline_allow_model=None,candidate_allow_model=None)
            def positive(task,trial,folder,binary,*args,**kwargs):return execute(task,trial,folder,binary,provider=None,control='solution')
            with patch('yoloeval.paired.load_provider',return_value=provider),patch('yoloeval.paired.execute',side_effect=positive):
                comparison=run_paired(config)
            paired_ok=comparison['joint_success_pairs']==1 and not comparison['promotion_candidate']
    finally:server.shutdown();server.server_close()
    # All sessions are owned by this check; verify their listening ports closed.
    ports_closed=True
    for runtime in instances:
        runtime.close()  # repeated cleanup must be harmless
        for port in (runtime.frontend,runtime.backend,runtime.web):
            with socket.socket() as sock:
                sock.settimeout(.2);ports_closed &= sock.connect_ex(('127.0.0.1',port))!=0
    result={'passed':timeout_ok and paired_ok and ports_closed,'paid_model_calls':0,'timeout_recorded':timeout_ok,
            'deadline':deadline,'cleanup_ports_closed':ports_closed,'paired_controls_passed':paired_ok,
            'note':'The timeout uses a local delayed fake provider; paired attempts use reference solutions and no generation.'}
    write_json(out/'validation.json',result);print(json.dumps(result,indent=2))
    return 0 if result['passed'] else 1


if __name__=='__main__':raise SystemExit(main())
