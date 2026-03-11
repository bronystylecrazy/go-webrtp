"""
MQTT keyframe viewer example.

Subscribes to a WebRTP keyframe MQTT topic and displays each JPEG/PNG payload
with OpenCV.
"""

import argparse
import threading
from urllib.parse import urlparse

import cv2
import numpy as np
import paho.mqtt.client as mqtt


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--broker", default="mqtt://10.1.221.198:1883", help="MQTT broker URL, e.g. mqtt://10.1.221.198:1883")
    parser.add_argument("--topic", default="webrtp/demoLoop2/keyframe", help="MQTT topic carrying raw jpg/png payloads")
    parser.add_argument("--window", default="WebRTP MQTT Keyframe", help="OpenCV window title")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    parsed = urlparse(args.broker)
    host = parsed.hostname or "localhost"
    port = parsed.port or (8883 if parsed.scheme == "mqtts" else 1883)
    username = parsed.username
    password = parsed.password
    use_tls = parsed.scheme == "mqtts"

    latest = {"image": None, "count": 0}
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
        array = np.frombuffer(msg.payload, dtype=np.uint8)
        image = cv2.imdecode(array, cv2.IMREAD_COLOR)
        if image is None:
            print(f"failed to decode payload from {msg.topic} ({len(msg.payload)} bytes)", flush=True)
            return
        with lock:
            latest["image"] = image
            latest["count"] += 1

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
        while True:
            frame = None
            count = 0
            with lock:
                if latest["image"] is not None:
                    frame = latest["image"].copy()
                count = latest["count"]
            if frame is not None:
                cv2.putText(frame, f"MQTT frames: {count}", (20, 40),
                            cv2.FONT_HERSHEY_SIMPLEX, 1.0, (0, 255, 0), 2)
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
