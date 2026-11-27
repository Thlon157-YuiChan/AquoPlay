#!/usr/bin/env python3
"""Stream a recording from a DTCP-IP recorder, falling back to direct HTTP for
recordings whose content encryption mode (E-EMI) is not supported by pydtcpip."""
import asyncio
import sys


def pick_resource(recording):
    resource = recording.original_resource
    if resource is not None:
        return resource
    for r in recording.resources:
        if getattr(r.protocol_info, "is_dtcp", False):
            return r
    if recording.resources:
        return recording.resources[0]
    return None


async def main() -> None:
    if len(sys.argv) < 3:
        print("Usage: stream.py <server-url> <recording-id>", file=sys.stderr)
        sys.exit(1)
    url = sys.argv[1]
    recording_id = sys.argv[2]

    try:
        from pydtcpip import DLNAClient, DTCPClient, DeviceStore
        from pydtcpip.dtcp.pcp import decrypt_packet as _original_decrypt_packet, ProtectedPacket, PCP_HEADER_SIZE
        import pydtcpip.dtcp.pcp as _pcp_module

        def _patched_decrypt_packet(packet, exchange_key):
            if packet.encryption_mode == 0 and packet.cipher_algorithm == 0:
                # E-EMI 0x0: content is in cleartext, just strip PCP header
                return packet.wire_bytes[PCP_HEADER_SIZE:PCP_HEADER_SIZE + packet.content_length]
            return _original_decrypt_packet(packet, exchange_key)

        _pcp_module.decrypt_packet = _patched_decrypt_packet
        # Patch the name in client module (imported by value)
        import pydtcpip.client as _client_module
        _client_module.decrypt_packet = _patched_decrypt_packet
    except ImportError:
        print("pydtcpip is not installed", file=sys.stderr)
        sys.exit(1)

    store = DeviceStore()
    creds = store.load_credentials()

    async with DLNAClient() as dlna:
        server = await dlna.get_server(url)
        recording = await server.get_metadata(recording_id)
        resource = pick_resource(recording)
        if resource is None:
            print("No playable resource found", file=sys.stderr)
            sys.exit(1)

        target_url = resource.url

        # Try DTCP-IP streaming first
        try:
            async with DTCPClient(creds, srm=store.load_srm(), on_srm_update=store.save_srm) as client:
                chunks_received = False

                async def on_data(data: bytes) -> None:
                    nonlocal chunks_received
                    chunks_received = True
                    sys.stdout.buffer.write(data)
                    sys.stdout.buffer.flush()

                await client.stream(resource, on_data)
            return
        except BrokenPipeError:
            return  # Player closed; not an error
        except Exception as ex:
            if "Unsupported content encryption mode" in str(ex):
                pass  # Fall through to direct HTTP streaming below
            else:
                print(f"DTCP stream failed: {ex}", file=sys.stderr)
                raise

        # Direct HTTP streaming for unsupported E-EMI (e.g., E-EMI 0x0)
        import urllib.request

        req = urllib.request.urlopen(target_url)
        try:
            while True:
                data = req.read(65536)
                if not data:
                    break
                sys.stdout.buffer.write(data)
                sys.stdout.buffer.flush()
        except BrokenPipeError:
            return  # Player closed; not an error


if __name__ == "__main__":
    asyncio.run(main())
