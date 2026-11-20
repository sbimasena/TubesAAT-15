import argparse
import json
from pathlib import Path


def read_fires(path):
    rows = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(rows, dict):
        if rows.get("partial") is not False or rows["next_cursor"] is not None:
            raise ValueError(f"{path}: use a complete export, following every Audit cursor")
        rows = rows["items"]
    if not isinstance(rows, list):
        raise ValueError(f"{path}: expected a JSON array of events")

    fires = {}
    for row in rows:
        if row["source"] != "SATELIT" or row["hazard_type"] != "WILDFIRE":
            raise ValueError(f"{path}: expected only SATELIT WILDFIRE events")
        attributes = row["attributes"]
        keys = attributes["detection_keys"]
        if not isinstance(keys, list) or any(not isinstance(key, str) for key in keys):
            raise ValueError(f"{path}: expected a list of detection keys")
        membership = tuple(sorted(set(keys)))
        if len(membership) < 2 or len(membership) != len(keys) or membership in fires:
            raise ValueError(f"{path}: invalid or repeated fire membership: {membership}")
        fields = {name: row[name] for name in ("area_name", "severity", "latitude", "longitude", "occurred_at")}
        fields.update({name: attributes[name] for name in ("detection_count", "max_confidence", "total_frp_mw")})
        fields["satellites"] = sorted(attributes["satellites"])
        fires[membership] = fields
    return fires


def main():
    parser = argparse.ArgumentParser(description="Compare canonical satellite fires against a reference export.")
    parser.add_argument("actual", type=Path)
    parser.add_argument("reference", type=Path)
    args = parser.parse_args()
    try:
        actual = read_fires(args.actual)
        reference = read_fires(args.reference)
    except (OSError, KeyError, TypeError, ValueError) as error:
        parser.error(str(error))

    mismatches = []
    for keys in sorted(actual.keys() & reference.keys()):
        fields = {
            name: {"actual": value, "reference": reference[keys][name]}
            for name, value in actual[keys].items()
            if value != reference[keys][name]
        }
        if fields:
            mismatches.append({"detection_keys": keys, "fields": fields})
    result = {
        "actual_count": len(actual),
        "reference_count": len(reference),
        "missing": sorted(reference.keys() - actual.keys()),
        "extra": sorted(actual.keys() - reference.keys()),
        "mismatches": mismatches,
    }
    print(json.dumps(result, indent=2, ensure_ascii=False))
    return int(bool(result["missing"] or result["extra"] or mismatches))


if __name__ == "__main__":
    raise SystemExit(main())
