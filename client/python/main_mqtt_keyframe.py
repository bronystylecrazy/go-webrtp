"""
MQTT keyframe viewer example.

Subscribes to a WebRTP keyframe MQTT topic and displays wrapped JPG payloads
with frame id metadata using OpenCV.
"""

import argparse
import struct
import threading
from urllib.parse import urlparse

import cv2
import numpy as np
import paho.mqtt.client as mqtt


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--broker", default="mqtt://10.1.221.198:1883", help="MQTT broker URL, e.g. mqtt://10.1.221.198:1883")
    parser.add_argument("--topic", default="webrtp/demoLoop2/keyframe", help="MQTT topic carrying wrapped jpg payloads")
    parser.add_argument("--window", default="WebRTP MQTT Keyframe", help="OpenCV window title")
    return parser.parse_args()


def decode_keyframe_payload(payload: bytes) -> tuple[int, str, bytes]:
    if len(payload) < 10:
        raise ValueError(f"payload too small: {len(payload)}")
    if payload[:4] != b"WKF1":
        raise ValueError("unexpected payload magic")
    frame_no = struct.unpack(">I", payload[4:8])[0]
    format_len = struct.unpack(">H", payload[8:10])[0]
    header_len = 10 + format_len
    if len(payload) < header_len:
        raise ValueError(f"payload missing format bytes: {len(payload)} < {header_len}")
    image_format = payload[10:header_len].decode("utf-8", errors="replace")
    return frame_no, image_format, payload[header_len:]

def main() -> None:
    args = parse_args()
    parsed = urlparse(args.broker)
    host = parsed.hostname or "localhost"
    port = parsed.port or (8883 if parsed.scheme == "mqtts" else 1883)
    username = parsed.username
    password = parsed.password
    use_tls = parsed.scheme == "mqtts"

    latest = {"payload": None, "count": 0, "frame_no": None, "format": ""}
    lock = threading.Lock()

    def on_connect(client: mqtt.Client, _userdata, _flags, reason_code, _properties=None):
        print(f"connected to mqtt rc={reason_code}, subscribing to {args.topic}", flush=True)
        client.subscribe(args.topic)

    def on_subscribe(_client: mqtt.Client, _userdata, mid: int, reason_codes, _properties=None):
        print(f"subscribed mid={mid} reason_codes={reason_codes}", flush=True)

    def on_disconnect(_client: mqtt.Client, _userdata, disconnect_flags, reason_code, _properties=None):
        print(f"disconnected from mqtt rc={reason_code} flags={disconnect_flags}", flush=True)

    def on_message(_client: mqtt.Client, _userdata, msg: mqtt.MQTTMessage):
        print(f"mqtt message topic={msg.topic} bytes={len(msg.payload)}", flush=True)
        try:
            frame_no, image_format, image_payload = decode_keyframe_payload(msg.payload)
        except ValueError as exc:
            print(f"failed to parse payload from {msg.topic}: {exc}", flush=True)
            return
        if image_format.lower() not in ("jpg", "jpeg"):
            print(f"skipping payload from {msg.topic}: expected jpg, got {image_format}", flush=True)
            return
        with lock:
            latest["payload"] = image_payload
            latest["count"] += 1
            latest["frame_no"] = frame_no
            latest["format"] = image_format

    client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2)
    if username:
        client.username_pw_set(username, password)
    if use_tls:
        client.tls_set()
    client.on_connect = on_connect
    client.on_subscribe = on_subscribe
    client.on_disconnect = on_disconnect
    client.on_message = on_message

    print(f"connecting to {args.broker} topic={args.topic}", flush=True)
    client.connect(host, port, keepalive=60)
    client.loop_start()

    cv2.namedWindow(args.window, cv2.WINDOW_NORMAL)
    try:
        displayed_frame_no = None
        displayed_frame = None
        displayed_format = ""
        while True:
            frame = None
            count = 0
            latest_frame_no = None
            latest_format = ""
            image_payload = None
            with lock:
                image_payload = latest["payload"]
                count = latest["count"]
                latest_frame_no = latest["frame_no"]
                latest_format = latest["format"]
            if image_payload is not None and latest_frame_no != displayed_frame_no:
                array = np.frombuffer(image_payload, dtype=np.uint8)
                decoded = cv2.imdecode(array, cv2.IMREAD_COLOR)
                if decoded is None:
                    print(f"failed to decode image payload frame={latest_frame_no} format={latest_format} ({len(image_payload)} bytes)", flush=True)
                else:
                    displayed_frame = decoded
                    displayed_frame_no = latest_frame_no
                    displayed_format = latest_format
            if displayed_frame is not None:
                frame = displayed_frame.copy()
            if frame is not None:
                cv2.putText(frame, f"Frame ID: {displayed_frame_no}", (20, 40),
                            cv2.FONT_HERSHEY_SIMPLEX, 1.0, (0, 200, 255), 2)
                cv2.putText(frame, f"MQTT frames: {count}", (20, 85),
                            cv2.FONT_HERSHEY_SIMPLEX, 1.0, (0, 255, 0), 2)
                cv2.putText(frame, f"Format: {displayed_format}", (20, 130),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.9, (255, 220, 120), 2)
                cv2.imshow(args.window, frame)
            else:
                waiting = np.zeros((480, 860, 3), dtype=np.uint8)
                waiting[:] = (24, 30, 46)
                cv2.putText(waiting, "Waiting for MQTT keyframes...", (40, 220),
                            cv2.FONT_HERSHEY_SIMPLEX, 1.0, (220, 220, 220), 2)
                cv2.putText(waiting, args.topic, (40, 270),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.8, (120, 200, 255), 2)
                cv2.imshow(args.window, waiting)
            if cv2.waitKey(1) & 0xFF == ord("q"):
                break
    finally:
        client.loop_stop()
        client.disconnect()
        cv2.destroyAllWindows()


if __name__ == "__main__":
    main()
