# SPDX-License-Identifier: MPL-2.0
"""Verify the public homepage, discovery files, and read-only demo."""

import json
import sys
import time
from urllib.request import urlopen
from xml.etree import ElementTree


CANONICAL = "https://bugsnitch.com/"


def fetch(origin, path):
    with urlopen(origin + path, timeout=10) as response:
        return response.read()


def verify(origin):
    home = fetch(origin, "/").decode()
    if f'<link rel="canonical" href="{CANONICAL}"' not in home or 'href="/llms.txt"' not in home:
        raise ValueError("Homepage discovery metadata is missing")
    if b"<svg" not in fetch(origin, "/brand/bugsnitch-logo.svg"):
        raise ValueError("Homepage logo is missing")
    if ("Sitemap: " + CANONICAL + "sitemap.xml").encode() not in fetch(origin, "/robots.txt"):
        raise ValueError("robots.txt does not reference the sitemap")
    sitemap = ElementTree.fromstring(fetch(origin, "/sitemap.xml"))
    locations = [item.text for item in sitemap.findall(".//{http://www.sitemaps.org/schemas/sitemap/0.9}loc")]
    if locations != [CANONICAL]:
        raise ValueError("The sitemap must list the canonical homepage")
    for path in ("/llms.txt", "/llms-full.txt"):
        if not fetch(origin, path).startswith(b"# Bugsnitch"):
            raise ValueError(f"{path} does not contain the Bugsnitch guide")
    if b'<meta name="robots" content="noindex"' not in fetch(origin, "/app/"):
        raise ValueError("The demo must opt out of search indexing")
    if not json.loads(fetch(origin, "/api/v1/state"))["capabilities"]["demo"]:
        raise ValueError("The public workspace must serve demo data")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("Provide the homepage origin to verify")
    for attempt in range(6):
        try:
            verify(sys.argv[1].rstrip("/"))
        except (OSError, ValueError, KeyError, ElementTree.ParseError) as error:
            if attempt == 5:
                sys.exit(str(error))
            time.sleep(5)
        else:
            print(f"Verified homepage, sitemap, robots.txt, LLM guides, and read-only demo on {sys.argv[1]}")
            break
