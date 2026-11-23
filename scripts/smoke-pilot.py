#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Check that two hosted pilot instances keep owner sessions and projects separate."""
from http.cookies import SimpleCookie
import json
import os
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def call(origin, route, *, body=None, cookie=None, expected=200):
    headers = {"Origin": origin, "Content-Type": "application/json"}
    if cookie:
        headers["Cookie"] = cookie
    data = json.dumps(body).encode() if body is not None else None
    request = Request(f"{origin}/api/v1/{route}", headers=headers, data=data)
    try:
        with urlopen(request, timeout=10) as response:
            assert response.status == expected, (route, response.status, expected)
            return response.headers, response.read()
    except HTTPError as exc:
        assert exc.code == expected, (route, exc.code, expected)
        return exc.headers, b""


def run(first, second):
    first_token = os.environ["BUGSNITCH_OWNER_TOKEN"]
    second_token = os.environ["BUGSNITCH_SECOND_OWNER_TOKEN"]
    assert first != second and first_token != second_token

    first_headers, _ = call(first, "login", body={"token": first_token})
    second_headers, _ = call(second, "login", body={"token": second_token})
    first_cookie, second_cookie = (
        SimpleCookie(headers["Set-Cookie"]).output(header="", attrs=[]).strip()
        for headers in (first_headers, second_headers)
    )
    call(first, "login", body={"token": second_token}, expected=401)
    call(second, "login", body={"token": first_token}, expected=401)
    call(first, "state", cookie=second_cookie, expected=401)
    call(second, "state", cookie=first_cookie, expected=401)

    for origin, cookie, label in ((first, first_cookie, "first"), (second, second_cookie, "second")):
        repository = f"fixture/pilot-{label}"
        call(origin, "projects", cookie=cookie, body={"name": label, "repository": repository}, expected=201)
    for origin, cookie, label in ((first, first_cookie, "first"), (second, second_cookie, "second")):
        _, snapshot = call(origin, "state", cookie=cookie)
        assert [project["repository"] for project in json.loads(snapshot)["projects"]] == [f"fixture/pilot-{label}"]
    print("Both pilot instances rejected the other owner's credential and session and kept projects separate.")


if __name__ == "__main__":
    run(*sys.argv[1:])
