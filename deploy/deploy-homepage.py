# SPDX-License-Identifier: MPL-2.0
"""Deploy a verified homepage image to the existing Bugsnitch service."""

import json
import os
import re
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


SERVICE_NAME = "projects/bugsnitch/locations/us-central1/services/bugsnitch"
SERVICE_URL = f"https://run.googleapis.com/v2/{SERVICE_NAME}"
RUNTIME_ACCOUNT = "bugsnitch-web-runtime@bugsnitch.iam.gserviceaccount.com"
IMAGE_PREFIX = "us-central1-docker.pkg.dev/bugsnitch/bugsnitch-web/homepage@sha256:"
TRAFFIC = [{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 100}]


def request(token, method="GET", update=None):
    url = SERVICE_URL
    data = None
    if update is not None:
        url += "?updateMask=template.containers,template.serviceAccount,traffic"
        data = json.dumps(update).encode()
    headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
    try:
        with urlopen(Request(url, data=data, headers=headers, method=method), timeout=20) as response:
            return json.load(response)
    except HTTPError as error:
        details = json.load(error).get("error", {})
        raise RuntimeError(f"Cloud Run {method} failed ({error.code}): {details.get('message', '')}") from None


def ready_revision(service, image):
    if service.get("reconciling"):
        return None
    terminal = service.get("terminalCondition", {})
    if terminal.get("state") == "CONDITION_FAILED":
        raise RuntimeError(terminal.get("message") or "Cloud Run reconciliation failed")
    template = service["template"]
    if template["containers"][0]["image"] != image or template.get("serviceAccount") != RUNTIME_ACCOUNT:
        return None
    created = service.get("latestCreatedRevision")
    ready = service.get("latestReadyRevision")
    if not ready or ready != created:
        return None
    destinations = [destination for destination in service.get("trafficStatuses", []) if destination.get("percent")]
    if len(destinations) == 1 and destinations[0]["percent"] == 100:
        revision = ready.rsplit("/", 1)[-1]
        if destinations[0].get("revision", "").rsplit("/", 1)[-1] == revision:
            return revision
    return None


def deploy(token, image):
    service = request(token)
    containers = service["template"]["containers"]
    if any(
        env.get("name") == "DATABASE_URL"
        for container in containers for env in container.get("env", [])
    ):
        return None
    if len(containers) != 1 or containers[0].get("name") != "homepage-1":
        raise RuntimeError("Expected the existing single-container homepage service")
    if any(target.get("tag") for target in service.get("traffic", [])):
        raise RuntimeError("The service has tagged traffic targets that require an explicit rollout")
    if service.get("traffic") == TRAFFIC:
        revision = ready_revision(service, image)
        if revision:
            return revision
    request(token, "PATCH", {
        "name": SERVICE_NAME,
        "etag": service["etag"],
        "template": {
            "containers": [{**containers[0], "image": image}],
            "serviceAccount": RUNTIME_ACCOUNT,
        },
        "traffic": TRAFFIC,
    })
    deadline = time.monotonic() + 420
    while time.monotonic() < deadline:
        revision = ready_revision(request(token), image)
        if revision:
            return revision
        time.sleep(5)
    raise RuntimeError("Cloud Run did not route all traffic to the new ready revision within seven minutes")


if __name__ == "__main__":
    if len(sys.argv) != 2 or not re.fullmatch(re.escape(IMAGE_PREFIX) + r"[a-f0-9]{64}", sys.argv[1]):
        sys.exit("Provide the Bugsnitch homepage image by its Artifact Registry SHA-256 digest")
    try:
        revision = deploy(os.environ["CLOUD_RUN_ACCESS_TOKEN"], sys.argv[1])
        if revision is not None:
            print(revision)
    except (KeyError, OSError, RuntimeError, ValueError) as error:
        sys.exit(str(error))
