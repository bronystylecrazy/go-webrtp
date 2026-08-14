import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WebRtpClient } from "./client";

type SocketState = "connecting" | "open" | "closing" | "closed";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static url = "ws://test/stream";
  static CONNECTING: SocketState = "connecting";
  static OPEN: SocketState = "open";
  static CLOSING: SocketState = "closing";
  static CLOSED: SocketState = "closed";

  binaryType = "arraybuffer";
  readyState: SocketState = "connecting";
  closeCalls = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  open(): void {
    this.readyState = "open";
    this.onopen?.(new Event("open"));
  }

  receive(data: ArrayBuffer): void {
    this.onmessage?.(new MessageEvent("message", { data }));
  }

  close(): void {
    this.closeCalls++;
    this.readyState = "closed";
    this.onclose?.(new CloseEvent("close"));
  }
}

function installFakeSocket() {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
}

function lastSocket(): FakeWebSocket {
  return FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
}

describe("WebRtpClient idle watchdog", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    installFakeSocket();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reconnects when an open socket stays silent past the idle timeout", () => {
    const events: string[] = [];
    const client = new WebRtpClient(FakeWebSocket.url, { idleTimeoutMs: 5000 });
    client.onEvent((event) => events.push(event.type));

    const first = lastSocket();
    first.open();
    expect(first.closeCalls).toBe(0);

    vi.advanceTimersByTime(5000);

    expect(first.closeCalls).toBe(1);
    expect(events).toContain("idle-timeout");

    vi.advanceTimersByTime(1000);
    const second = lastSocket();
    expect(second).not.toBe(first);
    expect(second.readyState).toBe("connecting");

    client.close();
  });

  it("keeps a socket alive while messages keep arriving", () => {
    const client = new WebRtpClient(FakeWebSocket.url, { idleTimeoutMs: 5000 });

    const socket = lastSocket();
    socket.open();
    // Paused so incoming messages skip the decode pipeline, which needs
    // WebCodecs; the idle watchdog re-arms before that.
    client.pause();

    // Messages arrive just inside the idle window, repeatedly.
    for (let tick = 0; tick < 5; tick++) {
      vi.advanceTimersByTime(4000);
      socket.receive(new ArrayBuffer(8));
    }
    vi.advanceTimersByTime(4000);

    expect(socket.closeCalls).toBe(0);

    client.close();
  });

  it("does not fire after close", () => {
    const client = new WebRtpClient(FakeWebSocket.url, { idleTimeoutMs: 5000 });

    const socket = lastSocket();
    socket.open();
    client.close();

    vi.advanceTimersByTime(10000);

    expect(socket.closeCalls).toBe(1); // the explicit close, never the watchdog
  });
});
