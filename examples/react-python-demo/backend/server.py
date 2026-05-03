import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


HOST = "0.0.0.0"
PORT = int(os.environ.get("PORT", "8000"))


class Handler(BaseHTTPRequestHandler):
    server_version = "DevupDemoBackend/1.0"

    def do_GET(self):
        if self.path == "/api/health":
            self.respond_json(
                {
                    "status": "ok",
                    "service": "backend",
                    "port": PORT,
                }
            )
            return
        if self.path == "/api/message":
            self.respond_json(
                {
                    "message": "Hello from the Python backend",
                    "stack": "react-python-demo",
                }
            )
            return
        self.respond_json({"error": "not found"}, status=404)

    def log_message(self, fmt, *args):
        print(f"[backend] {self.address_string()} - {fmt % args}", flush=True)

    def respond_json(self, payload, status=200):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)


def main():
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print(f"[backend] listening on http://{HOST}:{PORT}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
