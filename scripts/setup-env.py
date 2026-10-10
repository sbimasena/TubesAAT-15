#!/usr/bin/env python3
"""Create local Compose credentials once; never overwrite an existing environment file."""
import argparse
import os
from pathlib import Path
import secrets


def create_environment(template, destination):
    lines = template.read_text().splitlines()
    values = dict(line.split("=", 1) for line in lines if line and not line.startswith("#") and "=" in line)
    for key in ("JWT_SIGNING_SECRET", "AUTH_INTERNAL_SECRET", "MEDIA_CLIENT_PASSWORD",
                "FIELD_TEAM_CLIENT_PASSWORD", "INTERNAL_OPS_CLIENT_PASSWORD", "POSTGRES_PASSWORD",
                "RABBITMQ_DEFAULT_PASS", "BMKG_API_KEY", "PVMBG_TOKEN"):
        values[key] = secrets.token_urlsafe(32)
    values["DATABASE_URL"] = (f"postgres://{values['POSTGRES_USER']}:{values['POSTGRES_PASSWORD']}"
                              f"@canonical-store:5432/{values['POSTGRES_DB']}?sslmode=disable")
    values["BROKER_URL"] = (f"amqp://{values['RABBITMQ_DEFAULT_USER']}:{values['RABBITMQ_DEFAULT_PASS']}"
                            "@message-broker:5672/")
    try:
        descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        return False
    with os.fdopen(descriptor, "w") as output:
        output.write("\n".join(line.split("=", 1)[0] + "=" + values[line.split("=", 1)[0]]
                               if line and not line.startswith("#") and "=" in line else line
                               for line in lines) + "\n")
    return True


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=Path(".env"))
    args = parser.parse_args()
    created = create_environment(Path(__file__).resolve().parent.parent / ".env.example", args.env_file)
    print(f"{args.env_file}: " + ("created with local credentials" if created else "already exists; kept as-is"))
