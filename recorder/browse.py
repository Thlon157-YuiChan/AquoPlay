#!/usr/bin/env python3
"""Enumerate folders and recordings on a DTCP-IP recorder, preserving folder
structure. Uses paginated browse (browse_all) so no recordings are missed."""
import asyncio
import json
import sys


async def main() -> None:
    if len(sys.argv) < 2:
        print("Usage: browse.py <server-url>", file=sys.stderr)
        sys.exit(1)
    url = sys.argv[1]

    try:
        from pydtcpip import DLNAClient
        from pydtcpip.models import Container, Recording
    except ImportError:
        print("pydtcpip is not installed", file=sys.stderr)
        sys.exit(1)

    async with DLNAClient() as dlna:
        server = await dlna.get_server(url)

        # Recursively collect containers and recordings
        folders = []
        recordings = []
        visited = set()
        pending = ["0"]

        while pending:
            container_id = pending.pop(0)
            if container_id in visited:
                continue
            visited.add(container_id)

            try:
                items = await server.browse_all(container_id)
            except Exception:
                continue

            for item in items:
                if isinstance(item, Container):
                    folders.append({
                        "id": item.id,
                        "title": item.title or item.id,
                        "parent_id": item.parent_id if item.parent_id else "0",
                        "child_count": item.child_count,
                    })
                    pending.append(item.id)
                elif isinstance(item, Recording):
                    resource = item.original_resource
                    size = 0
                    if resource is not None and resource.attributes.get("size"):
                        try:
                            size = int(resource.attributes["size"])
                        except (TypeError, ValueError):
                            size = 0
                    duration = item.duration
                    if duration is None and resource is not None:
                        duration = resource.duration
                    encrypted = bool(resource is not None and getattr(resource.protocol_info, "is_dtcp", False))
                    url_val = resource.url if resource is not None else ""
                    recordings.append({
                        "id": item.id,
                        "title": item.title or "",
                        "parent_id": item.parent_id or container_id,
                        "date": item.date or "",
                        "duration": duration or "",
                        "size": size,
                        "encrypted": encrypted,
                        "url": url_val,
                    })

        # Deduplicate recordings by reference_id (already handled by iter, but browse_all doesn't dedup)
        seen = set()
        unique_recordings = []
        for r in recordings:
            if r["id"] not in seen:
                seen.add(r["id"])
                unique_recordings.append(r)

        print(json.dumps({"folders": folders, "recordings": unique_recordings}, ensure_ascii=False))


if __name__ == "__main__":
    asyncio.run(main())
