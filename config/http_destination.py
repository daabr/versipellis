import http.server
import json

PORT = 8080


class SimpleRequestHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(10240).decode("utf-8")

        match self.headers.get("Content-Type", ""):
            case "application/json":
                print("\n" + json.dumps(json.loads(body), indent=2))
            case "application/x-ndjson":
                for line in body.strip().splitlines():
                    print("\n" + json.dumps(json.loads(line), indent=2))

        self.send_response(200)
        self.end_headers()


if __name__ == "__main__":
    server = http.server.HTTPServer(("", PORT), SimpleRequestHandler)
    print(f"Simple HTTP server listening on port {PORT}")
    server.serve_forever()
