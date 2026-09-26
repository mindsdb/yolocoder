"""Local recording proxy: the real credential never enters an app process."""
import json
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit


def load_provider():
    folder = Path.home()/'.config/yolocoder'
    config = json.loads((folder/'config.json').read_text())
    credential = json.loads((folder/'credentials.json').read_text())['api_key']
    if not credential:
        raise ValueError('The configured YoloCoder credential is empty')
    base = config['base_url'].rstrip('/')
    if urlsplit(base).scheme != 'https':
        raise ValueError('Live evaluation requires an HTTPS upstream provider')
    return {**config, 'base_url': base, 'api_key': credential}


def usage_of(body):
    usage = body.get('usage')
    if not isinstance(usage, dict):
        return None
    incoming = usage.get('input_tokens', usage.get('prompt_tokens'))
    outgoing = usage.get('output_tokens', usage.get('completion_tokens'))
    if not isinstance(incoming, int) or not isinstance(outgoing, int):
        return None
    details = usage.get('input_tokens_details', usage.get('prompt_tokens_details', {})) or {}
    cached = details.get('cached_tokens', 0)
    total = usage.get('total_tokens', incoming+outgoing)
    if not isinstance(cached,int) or not isinstance(total,int):return None
    consistent = 0 <= cached <= incoming and outgoing >= 0 and total == incoming+outgoing
    return {'input': incoming, 'output': outgoing, 'cached': cached, 'total': total,'consistent':consistent}


class Recorder:
    def __init__(self, provider, path, max_calls=30):
        self.provider, self.path, self.max_calls = provider, path, max_calls
        allowed_models={provider['model'],*provider.get('allow_models',[])}
        self.calls = []
        self.lock = threading.Lock()
        recorder = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                if self.headers.get('Authorization')!='Bearer eval-proxy-token':
                    self.send_error(401); return
                if self.path not in ('/v1/responses', '/responses', '/v1/chat/completions', '/chat/completions','/v1/decisions','/decisions'):
                    self.send_error(404); return
                length = int(self.headers.get('Content-Length', '0'))
                if not 0 < length <= 4*1024*1024:
                    self.send_error(413); return
                data = self.rfile.read(length)
                try:
                    request_body = json.loads(data)
                except ValueError:
                    self.send_error(400); return
                if request_body.get('model') not in allowed_models:
                    self.send_error(400, 'Model differs from the recorded run configuration'); return
                with recorder.lock:
                    if len(recorder.calls) >= recorder.max_calls:
                        self.send_error(429, 'Evaluation request limit reached'); return
                    index = len(recorder.calls)
                    recorder.calls.append({'index':index, 'complete':False})
                base = provider['base_url']
                suffix = self.path.removeprefix('/v1')
                endpoint = (base if base.endswith('/v1') else base+'/v1') + suffix
                started = time.monotonic()
                req = urllib.request.Request(endpoint, data=data, headers={'Content-Type':'application/json','Authorization':'Bearer '+provider['api_key'],'User-Agent':'YoloCoder-evals/1.0'})
                response_body, status = b'', 502
                upstream_status, response_origin, transport_error_type = None, 'upstream', None
                try:
                    try:
                        response = urllib.request.urlopen(req, timeout=300)
                        body_limit = 32*1024*1024
                    except urllib.error.HTTPError as error:
                        response, body_limit = error, 1024*1024
                    with response:
                        upstream_status = response.status
                        response_body = response.read(body_limit)
                        status = upstream_status
                except Exception as error:
                    # Headers may already say 200 when reading the body fails.
                    # A synthetic transport error must never retain that status.
                    status, response_origin = 502, 'recorder_transport_error'
                    transport_error_type = type(error).__name__
                    response_body = json.dumps({'error':{'message':str(error)}}).encode()
                # Never persist or return an upstream error that echoes a key.
                response_body = response_body.replace(provider['api_key'].encode(), b'[REDACTED]')
                try:
                    parsed = json.loads(response_body)
                except ValueError:
                    parsed = {}
                call = {'index':index,'complete':True,'status':status,'endpoint':suffix,'duration_s':time.monotonic()-started,
                        'upstream_status':upstream_status,'response_origin':response_origin,'transport_error_type':transport_error_type,
                        'requested_model':request_body.get('model'),'returned_model':parsed.get('model'),
                        'request_bytes':len(data),'response_bytes':len(response_body),'usage':usage_of(parsed),'usage_raw':parsed.get('usage')}
                with recorder.lock:
                    recorder.calls[index] = call
                    recorder.path.write_text(json.dumps(recorder.calls, indent=2))
                try:
                    self.send_response(status)
                    self.send_header('Content-Type','application/json')
                    self.send_header('Content-Length',str(len(response_body)))
                    self.end_headers(); self.wfile.write(response_body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        self.server = ThreadingHTTPServer(('127.0.0.1',0), Handler)
        # Drain in-flight upstream calls on close, including after an agent
        # timeout, so billed work is not silently omitted from the result.
        self.server.daemon_threads = False
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self):
        return f'http://127.0.0.1:{self.server.server_port}/v1'

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.server.shutdown(); self.server.server_close()

    def summary(self):
        with self.lock:
            calls = list(self.calls)
        known = [c['usage'] for c in calls if c.get('usage') is not None]
        return {'model_calls':len(calls),'failed_calls':sum(c.get('status',500)>=400 for c in calls),
                'usage_complete':len(known)==len(calls) and all(u['consistent'] for u in known),
                'usage_note':'Raw provider fields retained; cached > input or inconsistent totals make accounting incomplete.',
                'tokens_reported':{key:sum(u[key] for u in known) for key in ('input','output','cached','total')},
                'model_wait_s':sum(c.get('duration_s',0) for c in calls)}


def priced_cost(calls, prices):
    if not calls:
        return 0.0
    total = 0.0
    for call in calls:
        usage = call.get('usage')
        price = prices.get(call.get('requested_model'))
        if usage is None or price is None or not usage.get('consistent',True):
            return None
        if not all(k in price for k in ('input_per_million','output_per_million','cached_input_per_million')):
            return None
        total += ((usage['input']-usage['cached'])*price['input_per_million'] + usage['cached']*price['cached_input_per_million'] + usage['output']*price['output_per_million']) / 1_000_000
    return total
