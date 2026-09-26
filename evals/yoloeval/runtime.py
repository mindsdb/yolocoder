import json
import http.client
import os
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        return sock.getsockname()[1]


def request(url, body=None, timeout=10):
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None,
                                 headers={'Content-Type':'application/json'})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        text = response.read()
        return json.loads(text) if text else None


class Runtime:
    def __init__(self, workspace, output, binary, provider_url, model, dialect='responses'):
        self.workspace, self.output = workspace, output
        ports=set()
        while len(ports)<3: ports.add(port())
        self.frontend, self.backend, self.web = sorted(ports)
        self.url = f'http://127.0.0.1:{self.frontend}'
        self.control = f'http://127.0.0.1:{self.web}'
        self.events, self.lock = [], threading.Lock()
        self.stream_ready = threading.Event()
        self.stream = None
        self.event_thread = None
        self.stop_stream = False
        self.closed = False
        # Credentials and arbitrary user env vars are deliberately omitted.
        self.env = {k:os.environ[k] for k in ('PATH','LANG','LC_ALL','SystemRoot') if k in os.environ}
        # tsx opens a Unix socket here; macOS limits socket paths to 104 bytes.
        self.temporary = tempfile.TemporaryDirectory(prefix='yoloeval-',dir='/tmp')
        self.env.update({'HOME':str(output/'home'),'TMPDIR':self.temporary.name, 'NO_COLOR':'1',
            'YOLOCODER_NO_AUTOUPDATE':'1','FRONTEND_PORT':str(self.frontend),'BACKEND_PORT':str(self.backend),
            'OPENAI_BASE_URL':provider_url,'OPENAI_API_KEY':'eval-proxy-token','OPENAI_MODEL':model,
            'OPENAI_API_DIALECT':dialect})
        (output/'home').mkdir()
        self.log = (output/'runtime.log').open('w')
        self.proc = subprocess.Popen([str(binary),'--llm-from-env-vars','--web','--port',str(self.web)],cwd=workspace,
            env=self.env,stdin=subprocess.PIPE,stdout=self.log,stderr=subprocess.STDOUT,start_new_session=True)

    def ready(self, timeout=60):
        deadline = time.monotonic()+timeout
        while time.monotonic()<deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(f'YoloCoder exited during startup ({self.proc.returncode}); see runtime.log')
            try:
                state = request(self.control+'/state', timeout=1)
                if state['process']=='running' and request(self.url+'/api/health',timeout=1).get('ok'):
                    return
            except (OSError, ValueError, KeyError):
                pass
            time.sleep(.1)
        raise TimeoutError('App did not become ready; see runtime.log')

    def _events(self):
        try:
            self.stream = urllib.request.urlopen(self.control+'/events',timeout=600)
            self.stream_ready.set()
            event = None
            while not self.stop_stream:
                line = self.stream.readline().decode().strip()
                if line.startswith('event: '):
                    event = line[7:]
                elif line.startswith('data: '):
                    entry = {'at':time.monotonic(),'event':event,'data':json.loads(line[6:])}
                    with self.lock:
                        self.events.append(entry)
                elif not line and self.proc.poll() is not None:
                    break
        except (OSError, ValueError, http.client.HTTPException):
            self.stream_ready.set()

    def task(self, prompt, timeout):
        self.event_thread = threading.Thread(target=self._events,daemon=True);self.event_thread.start()
        if not self.stream_ready.wait(5):
            raise RuntimeError('Could not subscribe to agent events')
        # A state round-trip follows the stream headers; the server registers
        # its SSE subscriber immediately after flushing those headers.
        request(self.control+'/state')
        started = time.monotonic()
        request(self.control+'/chat',{'message':prompt},timeout=10)
        reply, error, saw_busy = None, None, False
        while time.monotonic()-started<timeout:
            with self.lock:
                events = list(self.events)
            for item in events:
                if item['event']=='chat':
                    data=item['data']
                    if data.get('role')=='assistant': reply=data
                    elif data.get('role')=='system' and data.get('text','').startswith('Error:'): error=data['text']
            try:
                state=request(self.control+'/state',timeout=2)
                saw_busy |= state['busy']
                if not state['busy'] and (reply is not None or error is not None or saw_busy):
                    break
            except OSError:
                if self.proc.poll() is not None:
                    error='Agent process exited';break
            time.sleep(.05)
        else:
            error=f'Task exceeded {timeout}s'
            # Persist the deadline event before cleanup can fail.
            (self.output/'deadline.json').write_text(json.dumps({'timeout_s':timeout,'agent_wall_s_at_deadline':time.monotonic()-started}))
            self.close()
        elapsed=time.monotonic()-started
        with self.lock:
            normalized=[{**e,'at_s':e['at']-started} for e in self.events]
        for e in normalized: e.pop('at',None)
        (self.output/'events.json').write_text(json.dumps(normalized,indent=2))
        final=[e for e in normalized if e['event']=='chat' and e['data'].get('role')=='assistant']
        return {'agent_wall_s':elapsed,'agent_reply_s':final[-1]['at_s'] if final else None,
                'agent_error':error,'reply':reply,'events':normalized}

    def close(self):
        if self.closed:return
        self.closed=True
        self.stop_stream=True
        try:
            if self.stream is not None:
                # Release the SSE connection before asking the web server to
                # shut down. shutdown() wakes a reader blocked in readline().
                try:
                    with socket.socket(fileno=os.dup(self.stream.fileno())) as connection:
                        connection.shutdown(socket.SHUT_RDWR)
                except (OSError,ValueError,AttributeError):pass
                if self.event_thread is not None:self.event_thread.join(timeout=2)
                if self.event_thread is None or not self.event_thread.is_alive():self.stream.close()
            if self.proc.poll() is None:
                try:os.killpg(self.proc.pid,signal.SIGTERM)
                except ProcessLookupError:pass
                try:self.proc.wait(timeout=4)
                except subprocess.TimeoutExpired:
                    try:os.killpg(self.proc.pid,signal.SIGKILL)
                    except ProcessLookupError:pass
                    self.proc.wait(timeout=3)
            # Never signal a group after reaping its parent: macOS can
            # return EPERM for a dead group, and its ID can be reused.
        finally:
            self.log.close()
            self.temporary.cleanup()

    def __enter__(self): return self
    def __exit__(self,*_args): self.close()
