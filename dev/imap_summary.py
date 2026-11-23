#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.9"
# dependencies = []
# ///
"""Summarise what the archive delivered to the local Dovecot.

For every folder: message count, how many carry the X-Gmail-Archive-ID header, and DUPLICATES
(the same ID twice in one folder, which must never happen). Read-only.

    uv run dev/imap_summary.py [--host localhost] [--port 1143] [--user archive] [--password secret]
"""
import argparse
import imaplib
import re
import sys
from collections import Counter


def folder_name(line):
    m = re.match(rb'\((?P<attrs>[^)]*)\) "(?P<sep>[^"]*)" (?P<name>.+)$', line)
    name = m.group("name").decode()
    return name[1:-1] if name.startswith('"') else name


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--host", default="localhost")
    p.add_argument("--port", type=int, default=1143)
    p.add_argument("--user", default="archive")
    p.add_argument("--password", default="secret")
    a = p.parse_args()

    imap = imaplib.IMAP4(a.host, a.port)
    imap.login(a.user, a.password)
    _, data = imap.list()
    folders = sorted(folder_name(x) for x in data if x)
    total, dups = 0, 0
    print("%-40s %8s %8s %s" % ("folder", "messages", "tagged", "duplicates"))
    for f in folders:
        typ, _ = imap.select('"%s"' % f, readonly=True)
        if typ != "OK":
            continue
        _, nums = imap.search(None, "ALL")
        ids = nums[0].split()
        seen = Counter()
        if ids:
            # one header per message, in chunks
            for i in range(0, len(ids), 500):
                chunk = b",".join(ids[i:i + 500]).decode()
                _, resp = imap.fetch(chunk, "(BODY.PEEK[HEADER.FIELDS (X-GMAIL-ARCHIVE-ID)])")
                for part in resp:
                    if isinstance(part, tuple):
                        for line in part[1].decode("utf-8", "replace").splitlines():
                            if line.lower().startswith("x-gmail-archive-id:"):
                                seen[line.split(":", 1)[1].strip()] += 1
        d = sum(c - 1 for c in seen.values() if c > 1)
        print("%-40s %8d %8d %d" % (f, len(ids), sum(seen.values()), d))
        total += len(ids)
        dups += d
    imap.logout()
    print("\ntotal copies: %d, duplicates: %d" % (total, dups))
    return 1 if dups else 0


if __name__ == "__main__":
    sys.exit(main())
