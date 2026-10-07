"""Human-readable checker progress on stderr; stdout remains machine-readable."""
from pathlib import Path
import sys
import time

_started = time.monotonic()
_label = Path(sys.argv[0]).stem.removeprefix("check-")


def progress(message):
    print(f"[{_label} +{time.monotonic() - _started:.0f}s] {message}", file=sys.stderr, flush=True)


def heartbeat(description, attempt, every=5):
    if attempt % every == 0:
        progress(f"Waiting: {description} (attempt {attempt + 1})")
