# @connectedtechco/webrtp

WebRTP WebSocket client for receiving video streams in the browser.

## Installation

```bash
npm install @connectedtechco/webrtp
```

## Usage

```javascript
import { WebRtpClient, createClient } from '@connectedtechco/webrtp';

const client = createClient('ws://localhost:8080/stream/main');

// Render to canvas
client.render(document.getElementById('canvas'));

// Get info updates
client.onInfo((info) => {
    console.log('Info:', info);
});

// Get raw frame data with frame number
client.onFrame((frameNo, data, isKey) => {
    console.log(`Frame ${frameNo}: ${data.byteLength} bytes, keyframe: ${isKey}`);
});

// Start receiving
client.start();
```

## API

### WebRtpClient

#### constructor(wsUrl: string)

Create a new client instance.

#### render(target: HTMLCanvasElement | HTMLElement): this

Set the render target. Can be a canvas element or any container element.

#### onInfo(callback: (info: WebRtpInfo) => void): void

Set callback for info updates (codec, frames, dropped, paused).

#### onFrame(callback: (frameNo: number, data: Uint8Array, isKey: boolean) => void): void

Set callback for raw frame data with frame number and keyframe flag.

#### info(): WebRtpInfo

Get current client info.

#### play(): void

Resume receiving frames.

#### pause(): void

Pause receiving frames.

#### close(): void

Close the connection and cleanup resources.

### createClient(wsUrl: string): WebRtpClient

Factory function to create a new client.
