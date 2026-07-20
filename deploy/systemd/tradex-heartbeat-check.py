#!/usr/bin/env python3
"""Alert Telegram when the GCS status.json heartbeat is stale."""

import datetime as dt
import json
import os
import re
import socket
import sys
import time
import urllib.parse
import urllib.request

CONFIG = "/opt/tradex/config/config.{}.yaml".format(os.environ.get("TRADEX_ENV", "demo"))
STATE = "/opt/tradex/data/heartbeat-alert-state.json"
REPEAT_SECONDS = 15 * 60


def config_values():
    status_object, timeout = "", 120
    with open(CONFIG, encoding="utf-8") as config:
        for line in config:
            match = re.match(r"^\s*status_object:\s*[\"']?([^\"'#\s]+)", line)
            if match:
                status_object = match.group(1)
            match = re.match(r"^\s*liveness_timeout:\s*([0-9]+)([smh])", line)
            if match:
                value, unit = int(match.group(1)), match.group(2)
                timeout = value * {"s": 1, "m": 60, "h": 3600}[unit]
    if not status_object.startswith("gs://"):
        raise ValueError("observability.status_object is not configured")
    return status_object, timeout


def access_token():
    request = urllib.request.Request(
        "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token",
        headers={"Metadata-Flavor": "Google"},
    )
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)["access_token"]


def parse_rfc3339(value):
    """Parse Go encoding/json time.Time (RFC3339Nano) for Python <3.11.

    Go trims trailing fractional zeros, so as_of may have 1–9 fractional digits.
    datetime.fromisoformat only accepts exactly 0 or 6 digits on older Pythons.
    """
    s = value.strip().replace("Z", "+00:00")
    if "." in s:
        head, rest = s.split(".", 1)
        frac = ""
        i = 0
        while i < len(rest) and rest[i].isdigit():
            frac += rest[i]
            i += 1
        s = f"{head}.{(frac + '000000')[:6]}{rest[i:]}"
    return dt.datetime.fromisoformat(s)


def heartbeat_as_of(status_object):
    bucket, object_name = status_object[5:].split("/", 1)
    encoded = urllib.parse.quote(object_name, safe="")
    url = f"https://storage.googleapis.com/download/storage/v1/b/{bucket}/o/{encoded}?alt=media"
    request = urllib.request.Request(url, headers={"Authorization": f"Bearer {access_token()}"})
    with urllib.request.urlopen(request, timeout=10) as response:
        doc = json.load(response)
    return parse_rfc3339(doc["as_of"])


def send_telegram(message):
    token, chat_id = os.environ.get("TELEGRAM_BOT_TOKEN"), os.environ.get("TELEGRAM_CHAT_ID")
    if not token or not chat_id:
        return
    payload = json.dumps({"chat_id": chat_id, "text": message}).encode()
    request = urllib.request.Request(
        f"https://api.telegram.org/bot{token}/sendMessage", data=payload,
        headers={"Content-Type": "application/json"}, method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        response.read()


def read_state():
    try:
        with open(STATE, encoding="utf-8") as state:
            return json.load(state)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def main() -> int:
    now = dt.datetime.now(dt.timezone.utc)
    try:
        status_object, timeout = config_values()
        age = (now - heartbeat_as_of(status_object)).total_seconds()
        problem = "" if age <= timeout else "heartbeat stale"
        detail = "" if not problem else f"heartbeat age {int(age)}s exceeds {timeout}s"
    except Exception as error:
        problem = "heartbeat check failed"
        detail = str(error)

    state = read_state()
    if not problem:
        if state:
            with open(STATE, "w", encoding="utf-8") as out:
                json.dump({}, out)
        return 0

    if state.get("problem") == problem and time.time() - state.get("alerted_at", 0) < REPEAT_SECONDS:
        return 0
    message = (
        "tradex HEARTBEAT STALE\n"
        f"host: {socket.gethostname()}\n"
        f"{problem}: {detail}\n"
        "Inspect: sudo systemctl status tradex.service && sudo journalctl -u tradex.service -n 100 --no-pager"
    )
    try:
        send_telegram(message)
    except Exception as error:
        print(f"tradex heartbeat Telegram alert failed: {error}", file=sys.stderr)
    with open(STATE, "w", encoding="utf-8") as out:
        json.dump({"problem": problem, "alerted_at": time.time()}, out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
