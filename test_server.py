#!/usr/bin/env python3
"""
Root launcher for the CrawlSync Test HTTP Server.
Delegates to testsite/server.py.
"""
import os
import sys

# Ensure testsite directory is on python path
root_dir = os.path.dirname(os.path.abspath(__file__))
testsite_dir = os.path.join(root_dir, "testsite")
sys.path.insert(0, testsite_dir)

import server

if __name__ == "__main__":
    server.main()

