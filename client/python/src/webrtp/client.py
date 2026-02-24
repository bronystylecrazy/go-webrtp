import threading
import struct
import io
import os
from typing import Callable, Optional
import numpy as np
import av
from websocket import create_connection, WebSocketTimeoutException


class VideoDecoder:
    """H.264 video decoder using PyAV CodecContext - decodes NAL units directly."""

    def __init__(self, init_data: bytes):
        self._init_data = init_data
        self._codec_context: Optional[av.CodecContext] = None
        self._frame_count = 0
        self._avcc_config = self._extract_avcc(init_data)
        self._keyframe_buffer: list = []  # Buffer for keyframe NAL units
        self._has_keyframe = False
        self._sps: Optional[bytes] = None
        self._pps: Optional[bytes] = None
        self._extradata_sent = False

        if self._avcc_config:
            self._codec_context = av.CodecContext.create('h264', 'r')
            # Set extradata for decoder initialization
            self._codec_context.extradata = self._avcc_config
            self._codec_context.thread_type = 'AUTO'
            self._codec_context.pix_fmt = 'yuv420p'
            print(f"[Decoder] Created codec context with extradata len={len(self._avcc_config)}", flush=True)

    def _extract_avcc(self, data: bytes) -> Optional[bytes]:
        """Extract avcC configuration from init segment."""
        b = bytearray(data)
        for i in range(len(b) - 8):
            if i >= 4 and b[i] == 0x61 and b[i+1] == 0x76 and b[i+2] == 0x63 and b[i+3] == 0x43:
                box_size = (b[i-4] << 24) | (b[i-3] << 16) | (b[i-2] << 8) | b[i-1]
                pay_start = i + 4
                avcc_data = bytes(b[pay_start:pay_start + box_size - 8])
                return avcc_data
        return None

    def _extract_nal_units(self, data: bytes) -> list:
        """Extract NAL units from mdat - supports both AVCC (length prefix) and Annex-B (start code)."""
        b = bytearray(data)
        nal_units = []

        # First try AVCC format (4-byte big-endian length prefix)
        pos = 0
        print(f"[NAL] AVCC parse: data_len={len(b)}, first 16 bytes: {bytes(b[:16]).hex()}", flush=True)

        # Check if data starts with start code (Annex-B)
        has_start_code = False
        if len(b) >= 4 and b[0] == 0 and b[1] == 0:
            if b[2] == 1 or (len(b) >= 4 and b[2] == 0 and b[3] == 1):
                has_start_code = True
                print(f"[NAL] Detected Annex-B start code", flush=True)

        if has_start_code:
            # Try Annex-B format (start codes: 0x00000001 or 0x000001)
            print(f"[NAL] Trying Annex-B parsing", flush=True)
            pos = 0
            while pos < len(b) - 4:
                # Look for start code
                if b[pos] == 0 and b[pos+1] == 0:
                    if (b[pos+2] == 1) or (pos + 3 < len(b) and b[pos+2] == 0 and b[pos+3] == 1):
                        # Found start code
                        start = pos + (3 if b[pos+2] == 1 else 4)
                        print(f"[NAL] Annex-B: found start code at {pos}, data starts at {start}", flush=True)
                        # Find next start code or end
                        next_start = None
                        for j in range(start + 1, min(len(b) - 3, start + 100000)):  # Limit search
                            if b[j] == 0 and b[j+1] == 0:
                                if b[j+2] == 1 or (j + 3 < len(b) and b[j+2] == 0 and b[j+3] == 1):
                                    next_start = j
                                    break
                        if next_start:
                            nal_unit = bytes(b[start:next_start])
                            if nal_unit:
                                print(f"[NAL] Annex-B: extracted NAL, len={len(nal_unit)}, type={nal_unit[0] & 0x1f if nal_unit else -1}", flush=True)
                                nal_units.append(nal_unit)
                            pos = next_start
                        else:
                            nal_unit = bytes(b[start:])
                            if nal_unit:
                                print(f"[NAL] Annex-B: extracted final NAL, len={len(nal_unit)}, type={nal_unit[0] & 0x1f if nal_unit else -1}", flush=True)
                                nal_units.append(nal_unit)
                            break
                    else:
                        pos += 1
                else:
                    pos += 1

            if nal_units:
                print(f"[NAL] Annex-B: found {len(nal_units)} units", flush=True)
                return nal_units

        # Try AVCC format
        print(f"[NAL] Trying AVCC parsing", flush=True)
        while pos + 4 <= len(b):
            raw_len = bytes(b[pos:pos+4])
            nal_len_be = (b[pos] << 24) | (b[pos+1] << 16) | (b[pos+2] << 8) | b[pos+3]
            nal_len_le = b[pos] | (b[pos+1] << 8) | (b[pos+2] << 16) | (b[pos+3] << 24)
            print(f"[NAL] pos={pos}, raw={raw_len.hex()}, BE={nal_len_be}, LE={nal_len_le}", flush=True)

            # Use BE format (standard for AVCC)
            nal_len = nal_len_be
            if nal_len <= 0 or pos + 4 + nal_len > len(b):
                print(f"[NAL] AVCC: breaking, nal_len={nal_len}, pos+4+nal_len={pos+4+nal_len}, len(b)={len(b)}", flush=True)
                break
            if nal_len > 10000000:  # Sanity check
                print(f"[NAL] AVCC: nal_len too large, breaking", flush=True)
                break
            nal_unit = bytes(b[pos+4:pos+4 + nal_len])
            nal_units.append(nal_unit)
            print(f"[NAL] Extracted NAL unit, len={len(nal_unit)}", flush=True)
            pos += 4 + nal_len

        if nal_units:
            print(f"[NAL] AVCC: found {len(nal_units)} units", flush=True)
            return nal_units

        return []

    def decode(self, fragment: bytes) -> Optional[np.ndarray]:
        """Decode a fragment and return the frame."""
        if not self._codec_context:
            return None

        try:
            # Skip first 8 bytes (frameNo)
            payload = fragment[8:]
            if len(payload) < 100:
                return None

            b = bytearray(payload)

            # Find mdat box - search for 'mdat' directly in the data
            # Following JavaScript logic: look for 'mdat' at position i + 12
            mdat_content = None
            for i in range(len(b) - 16):
                # Check for 'mdat' at position i + 12
                if (b[i + 12] == 0x6D and b[i + 13] == 0x64 and
                    b[i + 14] == 0x61 and b[i + 15] == 0x74):
                    # Found mdat at position i + 12
                    # Size is at position i + 8
                    mdat_size = (b[i + 8] << 24) | (b[i + 9] << 16) | (b[i + 10] << 8) | b[i + 11]
                    # Content starts at i + 16
                    mdat_content = bytes(b[i + 16:i + 16 + mdat_size - 8])
                    break

            if mdat_content is None:
                print(f"[Decode] mdat not found, using full payload", flush=True)
                mdat_content = payload

            # Parse AVCC NAL units: 4-byte big-endian length prefix
            pos = 0
            while pos + 4 <= len(mdat_content):
                nal_len = (mdat_content[pos] << 24) | (mdat_content[pos + 1] << 16) | \
                          (mdat_content[pos + 2] << 8) | mdat_content[pos + 3]

                if nal_len <= 0 or pos + 4 + nal_len > len(mdat_content):
                    break
                if nal_len > 10000000:  # Sanity check
                    break

                nal_unit = mdat_content[pos + 4:pos + 4 + nal_len]
                nal_type = nal_unit[0] & 0x1f if nal_unit else -1

                # Handle SPS (7) and PPS (8) NAL units
                if nal_type == 7:
                    self._sps = nal_unit
                elif nal_type == 8:
                    self._pps = nal_unit

                # Convert from AVCC (4-byte length prefix) to Annex-B (start code)
                annexb_nal = b'\x00\x00\x00\x01' + nal_unit

                # Try to decode
                try:
                    packet = av.Packet(annexb_nal)
                    decoded_frames = list(self._codec_context.decode(packet))
                    if decoded_frames:
                        frame = decoded_frames[0]
                        self._frame_count += 1
                        return frame.to_ndarray(format='bgr24')
                except Exception:
                    pass

                pos += 4 + nal_len

        except Exception as e:
            print(f"[Decode] Exception: {e}", flush=True)
            pass

        return None


class WebRtpClient:
    def __init__(self, ws_url: str):
        self.ws_url = ws_url
        self.ws: Optional[object] = None
        self._running = False
        self._thread: Optional[threading.Thread] = None
        self._on_raw: Optional[Callable[[int, bytes, bool], None]] = None
        self._on_frame: Optional[Callable[[int, np.ndarray], None]] = None
        self._codec: Optional[str] = None
        self._init_data: Optional[bytes] = None
        self._decoder: Optional[VideoDecoder] = None
        self._width = 1920
        self._height = 1080
        self._last_frame_no = 0

    def on_raw(self, callback: Callable[[int, bytes, bool], None]) -> "WebRtpClient":
        self._on_raw = callback
        return self

    def on_frame(self, callback: Callable[[int, np.ndarray], None]) -> "WebRtpClient":
        self._on_frame = callback
        return self

    def start(self) -> "WebRtpClient":
        self.ws = create_connection(self.ws_url, timeout=10)
        self._running = True
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        return self

    def _run(self):
        try:
            while self._running:
                try:
                    data = self.ws.recv()
                    if isinstance(data, bytes):
                        self._handle_message(data)
                except WebSocketTimeoutException:
                    continue
                except Exception as e:
                    print(f"[WebRtp] recv error: {e}", flush=True)
                    break
        finally:
            self._running = False

    def _handle_message(self, data: bytes):
        if self._init_data is None:
            self._init_data = data
            codec, width, height = self._parse_init(data)
            self._codec = codec
            self._width = width or 1920
            self._height = height or 1080
            self._decoder = VideoDecoder(self._init_data)
            return

        if len(data) < 8:
            return

        frame_no = struct.unpack(">Q", data[:8])[0]
        self._last_frame_no = frame_no
        raw_data = data[8:]

        # Detect keyframe
        is_key = self._detect_keyframe(raw_data)

        if self._on_raw:
            self._on_raw(frame_no, raw_data, is_key)

        if self._on_frame and self._decoder:
            frame = self._decoder.decode(data)
            if frame is not None:
                self._on_frame(frame_no, frame)

    def _detect_keyframe(self, data: bytes) -> bool:
        """Detect if the data contains a keyframe (IDR NAL unit)."""
        b = bytearray(data)

        # Find mdat box first
        mdat_content = None
        for i in range(len(b) - 16):
            if (b[i + 12] == 0x6D and b[i + 13] == 0x64 and
                b[i + 14] == 0x61 and b[i + 15] == 0x74):
                mdat_content = bytes(b[i + 16:])
                break

        if mdat_content is None:
            mdat_content = data

        # Parse AVCC format: 4-byte length prefix
        pos = 0
        while pos + 4 <= len(mdat_content):
            nal_len = (mdat_content[pos] << 24) | (mdat_content[pos + 1] << 16) | \
                      (mdat_content[pos + 2] << 8) | mdat_content[pos + 3]

            if nal_len <= 0 or pos + 4 + nal_len > len(mdat_content):
                break
            if nal_len > 10000000:
                break

            if pos + 4 + nal_len <= len(mdat_content):
                nal_type = mdat_content[pos + 4] & 0x1f
                # NAL type 5 = IDR, 7 = SPS, 8 = PPS (important for decode)
                if nal_type == 5:
                    return True

            pos += 4 + nal_len

        return False

    def _parse_init(self, data: bytes):
        b = list(data)
        codec = "avc1.640028"
        width, height = 1920, 1080

        for i in range(len(b) - 8):
            if i >= 4 and b[i-4] == 0 and b[i-3] == 0 and b[i-2] == 0 and b[i-1] == 0 and \
               b[i] == 0x61 and b[i+1] == 0x76 and b[i+2] == 0x63 and b[i+3] == 0x43:
                box_size = (b[i-8] << 24) | (b[i-7] << 16) | (b[i-6] << 8) | b[i-5]
                pay_start = i + 4
                profile = f"{b[pay_start+1]:02x}"
                compat = f"{b[pay_start+2]:02x}"
                level = f"{b[pay_start+3]:02x}"
                codec = f"avc1.{profile}{compat}{level}"
                break

        for i in range(len(b) - 20):
            if b[i] == 0x74 and b[i+1] == 0x6b and b[i+2] == 0x68 and b[i+3] == 0x64:
                if len(b) > i + 84:
                    width = (b[i+76] << 8) | b[i+77]
                    height = (b[i+80] << 8) | b[i+81]
                break

        return codec, width, height

    def stop(self):
        self._running = False
        if self.ws:
            self.ws.close()

    @property
    def codec(self) -> Optional[str]:
        return self._codec

    @property
    def init_data(self) -> Optional[bytes]:
        return self._init_data


def create_client(ws_url: str) -> WebRtpClient:
    return WebRtpClient(ws_url)
