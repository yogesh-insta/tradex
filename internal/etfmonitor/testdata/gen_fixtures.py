"""Regenerate screen_py_fixtures.json from the reference Python screener.

The Go signal math is pinned to kite/betashares/screen.py (spec 21 §Testing).
These fixtures are produced by calling that module's own metrics() — never by
reimplementing it — so the pin cannot drift through a copied formula.

Run:
    cd /path/to/kite/betashares
    python3 /path/to/tradex/internal/etfmonitor/testdata/gen_fixtures.py

Writes screen_py_fixtures.json next to this script.
"""
import json
import os
import sys

import pandas as pd

sys.path.insert(0, os.getcwd())
import screen  # noqa: E402  (must be imported from the betashares dir)

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                   "screen_py_fixtures.json")


def series(vals):
    return pd.Series(vals, index=pd.bdate_range("2023-01-02", periods=len(vals)),
                     dtype=float)


def cases():
    n = 300
    # Deterministic compounding ramp: constant daily return, so vol ~ 0.
    yield "ramp", [100.0 * (1.001 ** i) for i in range(n)]
    # Flat, one step up, flat: zero recent momentum but positive 12m.
    yield "step", [100.0] * 150 + [120.0] * 150
    # Rise, fall, partial recovery: exercises max drawdown.
    yield "hump", ([100.0 * (1 + 0.004 * i) for i in range(100)] +
                   [140.0 * (1 - 0.003 * i) for i in range(100)] +
                   [98.0 * (1 + 0.002 * i) for i in range(100)])
    # UNADJUSTED SPLIT — the BBUS/BBOZ signature. Must be rejected, not ranked.
    yield "split", ([30.0 * (1 - 0.002 * i) for i in range(200)] + [2.0] +
                    [20.0 * (1 + 0.001 * i) for i in range(99)])


def main():
    out = {}
    for name, vals in cases():
        px = series(vals)
        m = screen.metrics(px)
        out[name] = {
            "closes": vals,
            "m3": None if pd.isna(m["m3"]) else m["m3"],
            "m6": None if pd.isna(m["m6"]) else m["m6"],
            "m12": None if pd.isna(m["m12"]) else m["m12"],
            "sma200_trend_up": None if m["above200"] is None else bool(m["above200"]),
            "vol": float(m["vol"]),
            "maxdd": float(m["maxdd"]),
            "days": int(m["days"]),
            "has_data_break": bool((px.pct_change().abs() > screen.DATA_BREAK).any()),
        }
    with open(OUT, "w") as f:
        json.dump(out, f, indent=2)
    print(f"wrote {OUT} ({len(out)} cases)")


if __name__ == "__main__":
    main()
