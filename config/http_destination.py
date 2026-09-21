import http.server
import json

PORT = 4884


class RequestPrinter(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        size = int(self.headers.get("Content-Length", 0))
        content_type = self.headers.get("Content-Type", "")
        body = self.rfile.read(size).decode("utf-8")

        match content_type:
            case "application/json":
                print("\n" + json.dumps(json.loads(body), indent=2))
            case "application/x-ndjson":
                for line in body.strip().splitlines():
                    print("\n" + json.dumps(json.loads(line), indent=2))
            case _:
                print(f"\nUnrecognized content type {content_type!r}\n\n{body}")

        self.send_response(200)
        self.end_headers()


if __name__ == "__main__":
    server = http.server.HTTPServer(("", PORT), RequestPrinter)
    print(f"Simple HTTP server listening on port {PORT}")
    server.serve_forever()
