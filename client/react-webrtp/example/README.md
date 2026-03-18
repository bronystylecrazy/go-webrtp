# react-webrtp example

This is a local Vite + React + TypeScript example for the `react-webrtp` package.
It resolves `react-webrtp` directly to `../src/index.ts`, so local package changes are picked up during development without publishing or linking.
The app owns a persistent `WebRtpClient`, and the `VideoPlayer` can be hidden by unmounting and later remounted onto the same client without reopening the WebSocket.

## Run

Install dependencies:

```bash
cd ..
bun install
```

Then run the example:

```bash
cd example
bun install
bun run dev
```

By default it connects to:

```text
ws://localhost:8080/stream/camera1
```

You can change the WebSocket URL in the page.
