"""Talk to the shared kw OpenBao (https://openbao.openbao.svc:8200) from a workstation.

The server image is distroless, so there is no exec path: start a kubectl
port-forward, verify TLS against the CA in Secret openbao/novaforge-admin
(hostname checked as SAN openbao.openbao.svc, never disabled), and send the
token as X-Vault-Token. The token never appears on a command line.
"""

import base64
import contextlib
import http.client
import json
import os
import socket
import ssl
import subprocess
import time

SERVER_NAME = "openbao.openbao.svc"
ENDPOINT = "https://" + SERVER_NAME + ":8200"
CONTEXT = os.environ.get("KUBE_CONTEXT", "kw")


def admin_secret():
    """Return (token, ca_pem) from the scoped admin Secret."""
    data = json.loads(
        subprocess.check_output(
            [
                "kubectl",
                "--context",
                CONTEXT,
                "-n",
                "openbao",
                "get",
                "secret",
                "novaforge-admin",
                "-o",
                "json",
            ],
            stderr=subprocess.PIPE,
        )
    )["data"]
    return base64.b64decode(data["token"]).decode(), base64.b64decode(
        data["ca.crt"]
    ).decode()


class Client:
    def __init__(self, port, token, ca):
        self.port, self.token = port, token
        self.ctx = ssl.create_default_context(cadata=ca)

    def call(self, method, path, data=None):
        port, ctx = self.port, self.ctx

        class Conn(http.client.HTTPSConnection):
            def connect(self):
                sock = socket.create_connection(("127.0.0.1", port), timeout=30)
                self.sock = ctx.wrap_socket(sock, server_hostname=SERVER_NAME)

        conn = Conn(SERVER_NAME, port, timeout=30)
        try:
            conn.request(
                method,
                "/v1/" + path,
                body=json.dumps(data) if data is not None else None,
                headers={
                    "X-Vault-Token": self.token,
                    "Content-Type": "application/json",
                },
            )
            resp = conn.getresponse()
            raw = resp.read()
        finally:
            conn.close()
        if resp.status >= 400:
            errors = json.loads(raw).get("errors") if raw.startswith(b"{") else None
            raise RuntimeError(
                "OpenBao %s %s: HTTP %d %s" % (method, path, resp.status, errors or "")
            )
        return json.loads(raw) if raw.strip() else {}


@contextlib.contextmanager
def session(token=None):
    """Yield a Client on a temporary port-forward; token defaults to the admin token."""
    admin, ca = admin_secret()
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    pf = subprocess.Popen(
        [
            "kubectl",
            "--context",
            CONTEXT,
            "-n",
            "openbao",
            "port-forward",
            "--address",
            "127.0.0.1",
            "svc/openbao",
            "%d:8200" % port,
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        for _ in range(100):
            if pf.poll() is not None:
                raise RuntimeError("kubectl port-forward to openbao exited")
            try:
                socket.create_connection(("127.0.0.1", port), timeout=1).close()
                break
            except OSError:
                time.sleep(0.2)
        else:
            raise RuntimeError("port-forward to openbao never became ready")
        yield Client(port, token or admin, ca)
    finally:
        pf.terminate()
        try:
            pf.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pf.kill()
