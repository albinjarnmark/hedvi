#!/usr/bin/env python3
"""Prepare a separate owner authenticator offline and privately feed its first proof to the operator CLI."""
import argparse
import base64
import getpass
import json
import os
import re
import secrets
import stat
import sys
import warnings
from pathlib import Path
from urllib.parse import quote, urlencode
from uuid import UUID


def prepare(args):
    email = args.email.strip().lower()
    if not re.fullmatch(r"[^@\s:]{1,64}@[^@\s:]+", email) or len(email.encode()) > 254:
        raise ValueError("Enter the exact verified account email.")
    parent = Path(args.file).absolute().parent.resolve()
    if any((directory / ".git").exists() for directory in (parent, *parent.parents)):
        raise ValueError("Choose a private enrollment path outside Git checkouts.")
    user_id = str(UUID(args.user))
    issuer = "Hedvi Owner (" + args.environment + ")"
    secret = base64.b32encode(secrets.token_bytes(20)).decode("ascii")
    label = quote(issuer + ":" + email, safe="")
    uri = "otpauth://totp/" + label + "?" + urlencode(
        {"issuer": issuer, "secret": secret, "algorithm": "SHA1", "digits": 6, "period": 30}
    )
    data = {"environment": args.environment, "user_id": user_id, "email": email, "secret": secret, "otpauth_uri": uri}
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW
    with os.fdopen(os.open(args.file, flags, 0o600), "w", encoding="utf-8") as file:
        json.dump(data, file, indent=2)
        file.write("\n")
    print("Created private enrollment file. Import its TOTP entry on the verified owner's device.", file=sys.stderr)


def activate(args):
    if not stat.S_ISFIFO(os.fstat(sys.stdout.fileno()).st_mode):
        raise ValueError("Pipe activation directly into the trusted operator command.")
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
    with os.fdopen(os.open(args.file, flags), "r", encoding="utf-8") as file:
        info = os.fstat(file.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 2048:
            raise ValueError("Use a private enrollment file owned by the current operator.")
        data = json.loads(file.read(2049))
    if not isinstance(data, dict) or set(data) != {"environment", "user_id", "email", "secret", "otpauth_uri"}:
        raise ValueError("Invalid enrollment file.")
    del data["otpauth_uri"]
    if not sys.stdin.isatty():
        raise ValueError("Run activation interactively from a trusted terminal.")
    with warnings.catch_warnings():
        warnings.simplefilter("error", getpass.GetPassWarning)
        data["code"] = getpass.getpass("First code from the separate Hedvi Owner authenticator: ")
    if not re.fullmatch(r"[0-9]{6}", data["code"]):
        raise ValueError("Enter a six-digit owner code.")
    json.dump(data, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    create = commands.add_parser("prepare")
    create.add_argument("--environment", required=True, choices=("local", "test", "staging", "production"))
    create.add_argument("--user", required=True)
    create.add_argument("--email", required=True)
    create.add_argument("--file", required=True)
    send = commands.add_parser("activate")
    send.add_argument("--file", required=True)
    args = parser.parse_args()
    try:
        if args.command == "prepare":
            prepare(args)
        else:
            activate(args)
    except (OSError, ValueError, getpass.GetPassWarning):
        parser.exit(1, "Owner enrollment failed. Check the command, private file permissions, terminal and account details.\n")


if __name__ == "__main__":
    main()
