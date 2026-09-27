"""Controlled Git upstream and signed webhook receiver for cluster acceptance."""
import hashlib
import hmac
import http.server
import json
import os
import subprocess
import threading
import urllib.parse

ROOT = "/data"
os.makedirs(ROOT, exist_ok=True)
def git(*args, cwd=ROOT):
    return subprocess.check_output(["git", *args], cwd=cwd, stderr=subprocess.STDOUT)
if not os.path.exists(ROOT + "/upstream.git"):
    git("init", "--bare", "--initial-branch=main", "upstream.git")
    git("clone", ROOT + "/upstream.git", "work")
    git("config", "user.email", "fixture@novaforge.test", cwd=ROOT + "/work")
    git("config", "user.name", "Migration fixture", cwd=ROOT + "/work")
    open(ROOT + "/work/README.md", "w").write("controlled migration fixture\n")
    git("add", ".", cwd=ROOT + "/work")
    git("commit", "-m", "initial history", cwd=ROOT + "/work")
    git("tag", "v1", cwd=ROOT + "/work")
    git("push", "origin", "main", "--tags", cwd=ROOT + "/work")
events = []
lock = threading.Lock()
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def reply(self, code, body):
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)
    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"ready": True})
        if self.path == "/events":
            with lock:
                return self.reply(200, events)
        return self.backend()
    def do_POST(self):
        if self.path == "/advance":
            with lock:
                path = ROOT + "/work"
                with open(path + "/README.md", "a") as out:
                    out.write("next commit\n")
                git("commit", "-am", "advance upstream", cwd=path)
                git("push", "origin", "main", cwd=path)
                return self.reply(200, {"sha": git("rev-parse", "HEAD", cwd=path).decode().strip()})
        if self.path == "/hooks":
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            expected = "sha256=" + hmac.new(b"fixture-signing-secret", body, hashlib.sha256).hexdigest()
            valid = hmac.compare_digest(self.headers.get("X-NovaForge-Signature", ""), expected)
            if not valid:
                return self.reply(401, {"error": "invalid signature"})
            with lock:
                events.append(json.loads(body))
            return self.reply(200, {"accepted": True})
        return self.backend()
    def backend(self):
        target = urllib.parse.urlsplit(self.path)
        if not target.path.startswith("/upstream.git/"):
            return self.reply(404, {"error": "not found"})
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        env = dict(os.environ, GIT_PROJECT_ROOT=ROOT, GIT_HTTP_EXPORT_ALL="1", REQUEST_METHOD=self.command,
                   PATH_INFO=target.path, QUERY_STRING=target.query, CONTENT_TYPE=self.headers.get("Content-Type", ""))
        result = subprocess.run(["git", "http-backend"], input=body, stdout=subprocess.PIPE, env=env, check=True)
        headers, content = result.stdout.split(b"\r\n\r\n", 1)
        pairs = [line.decode().split(":", 1) for line in headers.split(b"\r\n")]
        status = next((int(v.strip().split()[0]) for k,v in pairs if k.lower()=="status"), 200)
        self.send_response(status)
        for key,value in pairs:
            if key.lower() != "status": self.send_header(key, value.strip())
        self.end_headers()
        self.wfile.write(content)
http.server.ThreadingHTTPServer(("0.0.0.0", 8088), Handler).serve_forever()
