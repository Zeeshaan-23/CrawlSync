#!/usr/bin/env python3
"""
CrawlSync Test HTTP Server
==========================
A multithreaded test HTTP server designed for manually validating CrawlSync's
runtime pause, resume, stop, and checkpoint functionality.

Features:
- Powered by Python's standard library `http.server.ThreadingHTTPServer` for concurrent request handling.
- Configurable artificial delay (default 2.0s) per request to create a comfortable window
  for testing runtime pause, resume, manual checkpoints, and graceful stop.
- Timestamped request logging with URL, client IP, thread ID, and elapsed time.
- Serves index.html, page1.html, page2.html, page3.html, and page4.html.
- Preserves standard HTTP status codes (200, 404, 405) and Content-Type: text/html; charset=utf-8.
- Default port: 9000.
"""

import argparse
import datetime
import os
import sys
import threading
import time
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

# Default fallback HTML pages in case files on disk are missing
DEFAULT_PAGES = {
    "index.html": """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>CrawlSync Test Website - Home</title>
</head>
<body>
    <h1>Welcome to CrawlSync Test Website</h1>
    <p>This local site is designed for testing CrawlSync concurrency, pause, resume, stop, and checkpointing.</p>
    <nav>
        <ul>
            <li><a href="page1.html">Page 1: Architecture Overview</a></li>
            <li><a href="page2.html">Page 2: Concurrency & Worker Pool</a></li>
            <li><a href="page3.html">Page 3: Checkpointing & Recovery</a></li>
        </ul>
    </nav>
</body>
</html>
""",
    "page1.html": """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Page 1 - Architecture Overview</title>
</head>
<body>
    <h1>Page 1: Architecture Overview</h1>
    <p>CrawlSync coordinates a multithreaded worker pool with a centralized controller scheduler.</p>
    <nav>
        <ul>
            <li><a href="page2.html">Continue to Page 2</a></li>
            <li><a href="page4.html">Skip to Page 4</a></li>
            <li><a href="index.html">Back to Home</a></li>
        </ul>
    </nav>
</body>
</html>
""",
    "page2.html": """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Page 2 - Concurrency & Worker Pool</title>
</head>
<body>
    <h1>Page 2: Concurrency & Worker Pool</h1>
    <p>Worker goroutines fetch URLs concurrently via non-blocking channels and mutex-protected structures.</p>
    <nav>
        <ul>
            <li><a href="page3.html">Proceed to Page 3</a></li>
            <li><a href="page4.html">Proceed to Page 4</a></li>
            <li><a href="index.html">Back to Home</a></li>
        </ul>
    </nav>
</body>
</html>
""",
    "page3.html": """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Page 3 - Checkpointing & Recovery</title>
</head>
<body>
    <h1>Page 3: Checkpointing & Recovery</h1>
    <p>State checkpoints are atomically persisted to disk and verified across graceful or unexpected interruptions.</p>
    <nav>
        <ul>
            <li><a href="page4.html">Proceed to Page 4</a></li>
            <li><a href="index.html">Back to Home</a></li>
        </ul>
    </nav>
</body>
</html>
""",
    "page4.html": """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Page 4 - Crawl Leaf & Verification</title>
</head>
<body>
    <h1>Page 4: Crawl Leaf & Verification</h1>
    <p>This page serves as a test boundary leaf with cyclical cross-links back to earlier stages.</p>
    <nav>
        <ul>
            <li><a href="page1.html">Cycle to Page 1</a></li>
            <li><a href="index.html">Return to Home</a></li>
        </ul>
    </nav>
</body>
</html>
"""
}


class DelayedCrawlTestHandler(BaseHTTPRequestHandler):
    """HTTP request handler that logs requests and injects an artificial delay."""

    # Disables automatic logging to stderr in favor of custom formatted logger
    def log_message(self, format, *args):
        pass

    def _get_timestamp(self) -> str:
        return datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]

    def _resolve_page(self, path: str):
        """Maps URL path to target HTML filename and content."""
        clean_path = urlparse(path).path.strip("/")
        if clean_path == "" or clean_path == "index":
            filename = "index.html"
        else:
            filename = clean_path

        candidates = [filename]
        if not filename.endswith(".html"):
            candidates.append(f"{filename}.html")

        # 1. Check disk in site_dir
        site_dir = getattr(self.server, "site_dir", None)
        if site_dir:
            real_site_dir = os.path.realpath(site_dir)
            for cand in candidates:
                disk_path = os.path.realpath(os.path.join(site_dir, cand))
                # Security: prevent path traversal outside site_dir
                if os.path.commonpath([real_site_dir, disk_path]) == real_site_dir:
                    if os.path.isfile(disk_path):
                        try:
                            with open(disk_path, "rb") as f:
                                return cand, f.read()
                        except OSError:
                            pass

        # 2. Check embedded DEFAULT_PAGES fallback
        for cand in candidates:
            if cand in DEFAULT_PAGES:
                return cand, DEFAULT_PAGES[cand].encode("utf-8")

        return None, None

    def do_HEAD(self):
        self._handle_request(is_head=True)

    def do_GET(self):
        self._handle_request(is_head=False)

    def _handle_request(self, is_head: bool = False):
        start_time = time.time()
        client_ip, client_port = self.client_address[:2]
        thread_name = threading.current_thread().name
        requested_path = self.path
        delay = getattr(self.server, "delay", 2.0)

        ts_incoming = self._get_timestamp()
        print(f"[{ts_incoming}] [{thread_name}] INCOMING: {self.command} {requested_path} from {client_ip}:{client_port}", flush=True)

        # Apply configurable delay before sending response
        if delay > 0:
            print(f"[{self._get_timestamp()}] [{thread_name}] DELAY: Sleeping {delay:.2f}s before responding to {requested_path}...", flush=True)
            time.sleep(delay)

        filename, body = self._resolve_page(requested_path)
        self.close_connection = True
        elapsed = time.time() - start_time
        ts_finished = self._get_timestamp()

        if body is None:
            # 404 Not Found (returned as text/plain so crawler error handling detects failed download)
            not_found_msg = f"404 Not Found: The requested URL {requested_path} does not exist on this test server.\n".encode("utf-8")
            self.send_response(HTTPStatus.NOT_FOUND)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(not_found_msg)))
            self.send_header("Connection", "close")
            self.end_headers()
            if not is_head:
                self.wfile.write(not_found_msg)
            print(f"[{ts_finished}] [{thread_name}] RESPONSE: 404 NOT FOUND for {requested_path} (elapsed: {elapsed:.3f}s)", flush=True)
            return

        # 200 OK
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()

        if not is_head:
            self.wfile.write(body)

        print(f"[{ts_finished}] [{thread_name}] RESPONSE: 200 OK for {requested_path} [{filename}, {len(body)} bytes] (elapsed: {elapsed:.3f}s)", flush=True)


class CrawlTestServer(ThreadingHTTPServer):
    """Multithreaded HTTP server with custom parameters."""
    daemon_threads = True
    request_queue_size = 128

    def __init__(self, server_address, RequestHandlerClass, delay=2.0, site_dir=None):
        super().__init__(server_address, RequestHandlerClass)
        self.delay = delay
        self.site_dir = site_dir


def main():
    parser = argparse.ArgumentParser(
        description="CrawlSync Test HTTP Server with artificial delay for manual pause/resume/stop/checkpoint testing."
    )
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help="Server host/IP to bind (default: 127.0.0.1)"
    )
    parser.add_argument(
        "-p", "--port",
        type=int,
        default=9000,
        help="Server port to listen on (default: 9000)"
    )
    parser.add_argument(
        "-d", "--delay",
        type=float,
        default=2.0,
        help="Artificial response delay in seconds (default: 2.0)"
    )
    parser.add_argument(
        "--dir",
        default=None,
        help="Directory containing HTML test pages (default: same folder as script)"
    )

    args = parser.parse_args()

    # Determine site directory
    script_dir = os.path.dirname(os.path.abspath(__file__))
    site_dir = args.dir if args.dir else script_dir

    server_address = (args.host, args.port)

    try:
        httpd = CrawlTestServer(server_address, DelayedCrawlTestHandler, delay=args.delay, site_dir=site_dir)
    except OSError as err:
        print(f"Error starting server on {args.host}:{args.port}: {err}", file=sys.stderr)
        sys.exit(1)

    # Discover available HTML pages on disk
    html_files = []
    if os.path.isdir(site_dir):
        html_files = sorted([f for f in os.listdir(site_dir) if f.endswith(".html")])

    print("=" * 70)
    print(" CrawlSync Delayed Test HTTP Server")
    print("=" * 70)
    print(f"  Server URL:       http://{args.host}:{args.port}/")
    print(f"  Artificial Delay: {args.delay:.2f} seconds per HTML request")
    print(f"  Concurrency:      ThreadingHTTPServer (multithreaded)")
    print(f"  Serving Directory: {site_dir}")
    print(f"  Total HTML Pages: {len(html_files)} pages available on disk")
    print(f"  Primary Entry Points:")
    print(f"    - http://{args.host}:{args.port}/ (Homepage)")
    print(f"    - http://{args.host}:{args.port}/sitemap.html (Full Directory)")
    print(f"    - http://{args.host}:{args.port}/nonexistent-page.html (Simulated 404)")
    print("-" * 70)
    print("  Example CrawlSync CLI command:")
    print(f"    ./crawlsync -url http://{args.host}:{args.port}/ -allow-local -workers 4 -max-pages 50")
    print("-" * 70)
    print("  Press Ctrl+C to stop the test server.")
    print("=" * 70)
    sys.stdout.flush()

    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n[SHUTDOWN] Keyboard interrupt received. Stopping test server...", flush=True)
    finally:
        httpd.server_close()
        print("[SHUTDOWN] Test server stopped cleanly.", flush=True)


if __name__ == "__main__":
    main()

