import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


HOST = "0.0.0.0"
PORT = int(os.environ.get("PORT", "8000"))


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        payload = {"status": "ok", "runtime": "python", "pid": os.getpid()}
        body = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


def main():
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print("READY", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
