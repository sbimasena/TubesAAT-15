#!/usr/bin/env python3
"""Export all satellite events through the local Audit port-forward."""

from http.client import HTTPConnection, HTTPException
import json
import os
import sys
from urllib.parse import urlencode


def export_events(connection, token):
    query = {"source": "SATELIT", "limit": 1000}
    headers = {"Authorization": "Bearer " + token}
    seen_cursors = set()
    events = []
    while True:
        connection.request("GET", "/internal/audit/events?" + urlencode(query), headers=headers)
        response = connection.getresponse()
        if response.status != 200:
            raise ValueError(f"Audit HTTP {response.status}")
        page = json.load(response)
        if page["partial"] is not False or page["unavailable_units"] != []:
            raise ValueError("Audit storage is degraded; retry when all units are reachable")
        events.extend(page["items"])
        cursor = page["next_cursor"]
        if cursor is None:
            return events
        if not isinstance(cursor, str) or not cursor or cursor in seen_cursors:
            raise ValueError("Audit returned an invalid or repeated cursor")
        seen_cursors.add(cursor)
        query["cursor"] = cursor


if __name__ == "__main__":
    connection = HTTPConnection("127.0.0.1", 18083, timeout=10)
    try:
        events = export_events(connection, os.environ["AUDIT_TOKEN"])
    except (OSError, HTTPException, KeyError, TypeError, ValueError) as error:
        sys.exit(f"Satellite export failed: {error}")
    finally:
        connection.close()
    print(json.dumps(events, ensure_ascii=False, indent=2))
