import signal
import socket
import subprocess
import unittest
from unittest.mock import Mock,patch,call
from yoloeval.runtime import Runtime


class CleanupTests(unittest.TestCase):
    def runtime(self, alive):
        runtime=Runtime.__new__(Runtime)
        runtime.closed=False;runtime.stop_stream=False;runtime.stream=None;runtime.event_thread=None
        runtime.proc=Mock(pid=100);runtime.proc.poll.return_value=None if alive else 0
        runtime.log=Mock();runtime.temporary=Mock()
        return runtime

    def test_cleanup_is_idempotent_and_does_not_signal_a_reaped_group(self):
        r=self.runtime(True)
        with patch('yoloeval.runtime.os.killpg') as kill:
            def wait(**kwargs):r.proc.poll.return_value=0
            r.proc.wait.side_effect=wait
            r.close();r.close()
            self.assertEqual(kill.call_args_list,[call(100,signal.SIGTERM)])
        r.log.close.assert_called_once();r.temporary.cleanup.assert_called_once()

    def test_already_exited_process_never_receives_a_signal(self):
        r=self.runtime(False)
        with patch('yoloeval.runtime.os.killpg',side_effect=PermissionError(1,'Operation not permitted')) as kill:
            r.close();kill.assert_not_called()

    def test_stuck_live_process_is_killed(self):
        r=self.runtime(True);r.proc.wait.side_effect=[subprocess.TimeoutExpired('test',4),0]
        with patch('yoloeval.runtime.os.killpg') as kill:
            r.close();self.assertEqual(kill.call_args_list,[call(100,signal.SIGTERM),call(100,signal.SIGKILL)])

    def test_stream_is_released_before_shutdown(self):
        r=self.runtime(False)
        left,right=socket.socketpair()
        try:
            r.stream=Mock();r.stream.fileno.return_value=left.fileno()
            r.close();right.settimeout(.2);self.assertEqual(right.recv(1),b'')
            r.stream.close.assert_called_once()
        finally:left.close();right.close()


if __name__=='__main__':unittest.main()
