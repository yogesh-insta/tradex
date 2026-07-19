#!/usr/bin/env python3
"""Best-effort Telegram notification invoked by systemd OnFailure."""

import json
import os
import socket
import sys
import urllib.request


def main() -> int:
    token = os.environ.get("TELEGRAM_BOT_TOKEN")
    chat_id = os.environ.get("TELEGRAM_CHAT_ID")
    if not token or not chat_id:
        return 0

    unit = sys.argv[1] if len(sys.argv) > 1 else "tradex.service"
    message = (
        f"tradex PROCESS FAILURE\n"
        f"unit: {unit}\n"
        f"host: {socket.gethostname()}\n"
        "Inspect: sudo journalctl -u tradex.service -n 100 --no-pager"
    )
    payload = json.dumps({"chat_id": chat_id, "text": message}).encode()
    request = urllib.request.Request(
        f"https://api.telegram.org/bot{token}/sendMessage",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            response.read()
    except Exception as error:  # Alerting must not make systemd failure noisier.
        print(f"tradex failure Telegram alert failed: {error}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
