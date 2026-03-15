# WebRTP Python Client

Python client library for receiving WebRTP video streams via WebSocket.

## Installation

```bash
cd client/python
uv pip install -e .
```

## Usage

### With CV2 Display

```python
from webrtp import WebRtpClient
import cv2

client = WebRtpClient("ws://localhost:8080/stream/no/0")

# Get decoded frame with callback (cv2 numpy array)
client.on_frame(lambda frame_no, frame: cv2.imshow('video', frame))

client.start()

while True:
    cv2.waitKey(1)
```

### Raw Frame Data

```python
from webrtp import WebRtpClient

client = WebRtpClient("ws://localhost:8080/stream/no/0")

# Get raw frame data with callback
client.on_raw(lambda frame_no, data, is_key: print(f"Frame: {frame_no}, size: {len(data)}"))

client.start()
```

### MQTT Keyframe Viewer

When `keyframeSink` includes `mqtt`, the broker can receive wrapped `jpg`
payloads on the configured topic. View them with:

```bash
cd client/python
uv run python main_mqtt_keyframe.py \
  --broker mqtt://localhost:1883 \
  --topic webrtp/demoLoop2/keyframe
```

## API

### WebRtpClient

#### constructor(wsUrl: str)

Create a new client instance.

#### on_raw(callback: Callable[[int, bytes, bool], None]): self

Set callback for raw frame data with frame number, raw bytes, and keyframe flag.

#### on_frame(callback: Callable[[int, np.ndarray], None]): self

Set callback for decoded frame with frame number and numpy array (BGR format).

#### start(): self

Start receiving frames.

#### stop(): None

Stop receiving frames and close connection.

#### codec: Optional[str]

Property to get the codec information.

#### init_data: Optional[bytes]

Property to get the initialization data.
