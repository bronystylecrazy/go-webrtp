"""
WebRTP Client Example

Displays video stream from WebSocket with frame number overlay.
"""
import threading
import cv2
import numpy as np
from src.webrtp import WebRtpClient


def main():
    ws_url = "ws://localhost:8080/stream/no/0"

    print(f"Connecting to {ws_url}...", flush=True)

    latest_frame = None
    latest_frame_no = 0
    lock = threading.Lock()

    def on_frame(frame_no: int, frame: np.ndarray):
        """Decoded frame callback."""
        nonlocal latest_frame, latest_frame_no
        with lock:
            latest_frame = frame.copy()
            latest_frame_no = frame_no

    client = WebRtpClient(ws_url)
    client.on_frame(on_frame)
    client.start()

    # Wait for first frame
    while latest_frame is None:
        threading.Event().wait(0.1)

    print(f"First frame: {latest_frame_no}, shape={latest_frame.shape}", flush=True)

    window_name = "WebRTP"
    cv2.namedWindow(window_name, cv2.WINDOW_NORMAL)

    print("Displaying frames... Press 'q' to quit")

    try:
        while True:
            with lock:
                frame = latest_frame.copy()
                frame_no = latest_frame_no

            if frame is not None:
                # Overlay frame number
                cv2.putText(frame, f"Frame: {frame_no}", (20, 40),
                           cv2.FONT_HERSHEY_SIMPLEX, 1.0, (0, 255, 0), 2)
                cv2.imshow(window_name, frame)

            if cv2.waitKey(1) & 0xFF == ord('q'):
                break
    except KeyboardInterrupt:
        pass

    client.stop()
    cv2.destroyAllWindows()
    print("Stopped")


if __name__ == "__main__":
    main()
