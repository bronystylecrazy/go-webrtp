# react-webrtp

React and TypeScript package for playing WebRTP streams over WebSocket.

## Install

```bash
npm install react-webrtp react react-dom
```

## Build

```bash
npm run build
```

## Usage

```tsx
import { VideoPlayer, useWebRtpClient } from 'react-webrtp';

export function App() {
  const client = useWebRtpClient('ws://localhost:8080/stream/camera1');

  return (
    <>
      {client ? (
        <VideoPlayer
          client={client}
          style={{ width: 640, height: 360, background: '#000' }}
          onInfo={(info) => {
            console.log('stream info', info);
          }}
          onPlayerError={(error) => {
            console.error(error);
          }}
        />
      ) : null}
    </>
  );
}
```

`VideoPlayer` renders a native `<video>` element and internally decodes the WebRTP stream with `WebCodecs`.
`BackgroundPlayer` keeps the stream client running without rendering any DOM output, which is useful for frame processing, analytics, or connection state handling.
If you want the stream connection to outlive the player mount, keep the `WebRtpClient` in your app with `useWebRtpClient()` and pass it into `VideoPlayer`.
When `WebCodecs` is unavailable because the page is not in a secure context, the client falls back to Broadway for H.264 (`avc1`) streams.

## Exports

- `VideoPlayer`
- `BackgroundPlayer`
- `useWebRtpClient`
- `createClient`
- `WebRtpClient`
