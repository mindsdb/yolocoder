import json
import http.client
import io
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch

from yoloeval.provider import Recorder


class RecorderTests(unittest.TestCase):
    def test_roundtrip_usage_and_secret_boundary(self):
        seen=[]
        class Upstream(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_POST(self):
                seen.append((self.path,self.headers.get('Authorization'),self.headers.get('User-Agent')))
                self.rfile.read(int(self.headers['Content-Length']))
                data=json.dumps({'model':'test-model','usage':{'input_tokens':10,'output_tokens':3},'echo':'private-test-key'}).encode()
                self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
        upstream=ThreadingHTTPServer(('127.0.0.1',0),Upstream)
        thread=threading.Thread(target=upstream.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                output=Path(directory)/'calls.json'
                provider={'base_url':f'http://127.0.0.1:{upstream.server_port}','api_key':'private-test-key','model':'test-model','allow_models':['jev-test']}
                with Recorder(provider,output,max_calls=2) as recorder:
                    def send(model='test-model',key='eval-proxy-token',endpoint='/responses'):
                        req=urllib.request.Request(recorder.url+endpoint,data=json.dumps({'model':model,'input':'private prompt'}).encode(),headers={'Authorization':'Bearer '+key})
                        with urllib.request.urlopen(req) as response:return json.load(response)
                    with self.assertRaises(urllib.error.HTTPError) as rejected:send(key='wrong')
                    self.assertEqual(rejected.exception.code,401);rejected.exception.close()
                    with self.assertRaises(urllib.error.HTTPError) as rejected:send(model='wrong')
                    self.assertEqual(rejected.exception.code,400);rejected.exception.close()
                    response=send();self.assertEqual(response['echo'],'[REDACTED]')
                    send(model='jev-test',endpoint='/decisions')
                    with self.assertRaises(urllib.error.HTTPError) as rejected:send()
                    self.assertEqual(rejected.exception.code,429);rejected.exception.close()
                self.assertEqual(seen,[('/v1/responses','Bearer private-test-key','YoloCoder-evals/1.0'),('/v1/decisions','Bearer private-test-key','YoloCoder-evals/1.0')])
                self.assertEqual(recorder.summary()['tokens_reported']['total'],26)
                self.assertEqual(recorder.summary()['model_calls'],2)
                self.assertTrue(all(c['response_origin']=='upstream' and c['upstream_status']==200
                                    and c['transport_error_type'] is None for c in recorder.calls))
                saved=output.read_text()
                self.assertNotIn('private-test-key',saved);self.assertNotIn('private prompt',saved)
        finally:
            upstream.shutdown();upstream.server_close()

    def test_transport_failures_preserve_origin_and_consume_one_call(self):
        def response(status, body=None, error=None):
            value = Mock(status=status)
            value.__enter__ = Mock(return_value=value)
            value.__exit__ = Mock(return_value=False)
            value.read = Mock(return_value=body, side_effect=error)
            return value

        secret = 'private-test-key'
        read_error = ConnectionResetError('body reset ' + secret)
        http_read_error = urllib.error.HTTPError('https://example.invalid', 503, 'unavailable', {}, io.BytesIO())
        http_read_error.read = Mock(side_effect=ConnectionResetError('error body reset ' + secret))
        upstream_error_body = json.dumps({'error': {'message': 'upstream refusal ' + secret}}).encode()
        upstream_error = urllib.error.HTTPError('https://example.invalid', 524, 'timeout', {}, io.BytesIO(upstream_error_body))
        cases = [
            ('before_headers', None, urllib.error.URLError('connect failed ' + secret), 502, None, 'recorder_transport_error', 'URLError'),
            ('after_200_headers', response(200, error=read_error), None, 502, 200, 'recorder_transport_error', 'ConnectionResetError'),
            ('error_body_read', None, http_read_error, 502, 503, 'recorder_transport_error', 'ConnectionResetError'),
            ('upstream_refusal', None, upstream_error, 524, 524, 'upstream', None),
        ]
        for name, value, error, expected_status, upstream_status, origin, error_type in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                output = Path(directory)/'calls.json'
                provider = {'base_url':'https://example.invalid', 'api_key':secret, 'model':'test-model'}
                with patch('yoloeval.provider.urllib.request.urlopen', return_value=value, side_effect=error) as forward:
                    with Recorder(provider, output, max_calls=1) as recorder:
                        # http.client avoids mocking the local client together with the upstream.
                        def send():
                            connection = http.client.HTTPConnection('127.0.0.1', recorder.server.server_port, timeout=5)
                            try:
                                connection.request('POST', '/responses', json.dumps({'model':'test-model'}),
                                                   {'Authorization':'Bearer eval-proxy-token'})
                                result = connection.getresponse()
                                return result.status, result.read()
                            finally:
                                connection.close()
                        status, body = send()
                        self.assertEqual(status, expected_status)
                        self.assertNotIn(secret.encode(), body)
                        self.assertIn(b'[REDACTED]', body)
                        self.assertEqual(send()[0], 429)
                    forward.assert_called_once()
                if value is not None:
                    value.__exit__.assert_called_once()
                if isinstance(error, urllib.error.HTTPError):
                    self.assertTrue(error.closed)
                saved = json.loads(output.read_text())
                self.assertEqual(len(saved), 1)
                call = saved[0]
                self.assertTrue(call['complete'])
                self.assertEqual(call['status'], expected_status)
                self.assertEqual(call['upstream_status'], upstream_status)
                self.assertEqual(call['response_origin'], origin)
                self.assertEqual(call['transport_error_type'], error_type)
                self.assertEqual(call['requested_model'], 'test-model')
                self.assertIsNone(call['returned_model'])
                self.assertEqual(call['response_bytes'], len(body))
                self.assertGreaterEqual(call['duration_s'], 0)
                self.assertEqual(recorder.summary()['failed_calls'], 1)
                self.assertNotIn(secret, output.read_text())


if __name__=='__main__':unittest.main()
