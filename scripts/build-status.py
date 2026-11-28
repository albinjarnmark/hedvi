#!/usr/bin/env python3
"""Generate a public snapshot using only fixed, unauthenticated health endpoints."""
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import shutil
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
TARGETS = {"website": "https://hedvi.dev/", "dashboard": "https://app.hedvi.dev/login", "api": "https://api.hedvi.dev/readyz"}

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

def probe(item):
    name, url = item
    opener = urllib.request.build_opener(NoRedirect())
    for attempt in range(2):
        start = time.monotonic()
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "Hedvi-Status/1.0", "Cache-Control": "no-cache"})
            with opener.open(req, timeout=12) as response:
                data = response.read(65536)
                content_type = response.headers.get_content_type()
                ok = response.status == 200 and (json.loads(data).get("status") == "ready" if name == "api" else content_type == "text/html" and b"<html" in data.lower())
                if not ok:
                    raise ValueError("Unexpected health response")
            return {"id": name, "status": "passing", "duration_ms": round((time.monotonic()-start)*1000)}
        except Exception:
            if attempt == 0:
                time.sleep(2)
    # Do not publish response bodies, server exceptions or internal network data.
    return {"id": name, "status": "failing"}

def main():
    destination = ROOT / ".status-site"
    if destination.exists():
        shutil.rmtree(destination)
    destination.mkdir()
    source = ROOT / "apps/status"
    for name in ("index.html", "style.css", "status.mjs"):
        shutil.copy2(source/name, destination/name)
    shutil.copy2(ROOT/"brand/logo/hedvi-lockup-white.svg", destination/"logo.svg")
    incident = json.loads((source/"incident.json").read_text())
    if incident.get("state") not in ("none", "active"):
        raise ValueError("Invalid incident state")
    if incident["state"] == "active" and (not all(isinstance(incident.get(k),str) and incident[k].strip() for k in ("title","message","updated_at"))):
        raise ValueError("Active incident requires title, message and timestamp")
    if incident["state"] == "active":
        datetime.datetime.fromisoformat(incident["updated_at"].replace("Z","+00:00"))
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
        components = list(pool.map(probe, TARGETS.items()))
    snapshot = {"checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "components": components, "incident": incident}
    (destination/"status.json").write_text(json.dumps(snapshot,indent=2)+"\n")
    (destination/".nojekyll").write_text("")
    print(json.dumps(snapshot))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write("healthy="+str(all(c["status"]=="passing" for c in components)).lower()+"\n")

if __name__ == "__main__":
    main()
