import threading
from src.webrtp import WebRtpClient


def main():
    ws_url = "ws://localhost:8080/stream/no/0"

    latest = {}
    lock = threading.Lock()

    def on_raw(frame_no: int, data: bytes, is_key: bool):
        with lock:
            latest["no"] = frame_no
            latest["key"] = is_key
            latest["size"] = len(data)
        print(f"Frame {frame_no}: size={len(data)}, keyframe={is_key}", flush=True)

    client = WebRtpClient(ws_url)
    client.on_raw(on_raw)
    client.start()

    print(f"Connecting to {ws_url}...", flush=True)
    print("Press Ctrl+C to quit", flush=True)

    try:
        while True:
            threading.Event().wait(1)
    except KeyboardInterrupt:
        pass

    client.stop()
    print("Stopped", flush=True)


if __name__ == "__main__":
    main()
