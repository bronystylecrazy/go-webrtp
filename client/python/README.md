# WebRTP Python Client

Python client library for receiving WebRTP video streams via WebSocket.

## Installation

```bash
cd python
uv pip install -e .
```

## Usage

### CV2 Client

```python
from src.webrtp import WebRtpCv2Client
import cv2

client = WebRtpCv2Client("ws://localhost:8080/stream/main")

client.on_frame(lambda frame_no, frame, is_key: print(f"Frame: {frame_no}"))

client.start()

while True:
    cv2.waitKey(1)
```

### Raw Client

```python
from src.webrtp import WebRtpClient

client = WebRtpClient("ws://localhost:8080/stream/main")

client.on_raw(lambda frame_no, data, is_key: print(f"Frame: {frame_no}, size: {len(data)}"))

client.start()
```

## Examples

```bash
# Run CV2 example
python main.py

# Run raw example
python main_raw.py
```
