#!/usr/bin/env python3
"""Enumerate all recordings on a DTCP-IP recorder using pydtcpip's paginated browse."""
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
        from pydtcpip.operations import is_broadcast
    except ImportError:
        print("pydtcpip is not installed", file=sys.stderr)
        sys.exit(1)

    async with DLNAClient() as dlna:
        server = await dlna.get_server(url)
        recordings = []
        async for recording in server.iter_recordings():
            resource = recording.original_resource
            if resource is None:
                for r in recording.resources:
                    if getattr(r.protocol_info, "is_dtcp", False):
                        resource = r
                        break
                if resource is None and recording.resources:
                    resource = recording.resources[0]
            size = 0
            if resource is not None and resource.attributes.get("size"):
                try:
                    size = int(resource.attributes["size"])
                except (TypeError, ValueError):
                    size = 0
            duration = recording.duration
            if duration is None and resource is not None:
                duration = resource.duration
            encrypted = bool(resource is not None and resource.protocol_info.is_dtcp)
            resource_url = resource.url if resource is not None else ""
            # Extract raw ccnum from source_xml
            ccnum = None
            if recording.source_xml:
                import re as _re
                m = _re.search(r'shp:ccnum[^>]*>([^<]+)<', recording.source_xml)
                if m:
                    ccnum = m.group(1).strip()
            recordings.append(
                {
                    "id": recording.id,
                    "title": recording.title or "",
                    "date": recording.date or "",
                    "duration": duration or "",
                    "size": size,
                    "encrypted": encrypted,
                    "url": resource_url,
                    "broadcast": is_broadcast(recording),
                    "ccnum": ccnum,
                }
            )
        print(json.dumps(recordings, ensure_ascii=False))

if __name__ == "__main__":
    asyncio.run(main())
