export interface WebRtpInfo {
  playing?: boolean;
  frameNo?: number;
  codec?: string;
  width?: number;
  height?: number;
  dropped?: number;
  paused?: boolean;
}

export interface WebRtpClientOptions {
  autoReconnect?: boolean;
  reconnectDelayMs?: number;
  maxReconnectDelayMs?: number;
  lateFrameThreshold?: number;
  maxPendingDecode?: number;
}

export interface WebRtpEvent {
  type: 'connecting' | 'open' | 'close' | 'error' | 'reconnect-scheduled' | 'decoder-selected' | 'decoder-stalled';
  wsUrl: string;
  reconnectDelayMs?: number;
  event?: Event | CloseEvent;
  decoder?: 'webcodecs';
  codec?: string;
}

export type WebRtpInfoCallback = (info: WebRtpInfo) => void;
export type WebRtpFrameCallback = (frameNo: number, data: Uint8Array, isKey: boolean) => void;
export type WebRtpErrorCallback = (error: Error) => void;
export type WebRtpEventCallback = (event: WebRtpEvent) => void;

interface AvcDisplayRect {
  codedWidth: number;
  codedHeight: number;
  width: number;
  height: number;
  cropLeft: number;
  cropTop: number;
}

export class WebRtpClient {
  private readonly wsUrl: string;
  private readonly options: Required<WebRtpClientOptions>;
  private ws: WebSocket | null = null;
  private canvas: HTMLCanvasElement | null = null;
  private ctx: CanvasRenderingContext2D | null = null;
  private videoEl: HTMLVideoElement | null = null;
  private canvasHost: HTMLElement | null = null;
  private canvasStream: MediaStream | null = null;
  private decoder: VideoDecoder | null = null;
  private codec: string | null = null;
  private description: ArrayBuffer | null = null;
  private avcParameterSets: Uint8Array[] = [];
  private avcDisplayRect: AvcDisplayRect | null = null;
  private frameIndex = 0;
  private lastSourceFrameNo = 0;
  private initDone = false;
  private firstFrame = true;
  private dropped = 0;
  private paused = false;
  private closed = false;
  private reconnectTimer: number | null = null;
  private decoderWatchdogTimer: number | null = null;
  private reconnectDelay: number;
  private pendingDecode = 0;
  private waitingForKeyframe = false;
  private decodedPictureCount = 0;
  private readonly infoListeners = new Set<WebRtpInfoCallback>();
  private readonly frameListeners = new Set<WebRtpFrameCallback>();
  private readonly errorListeners = new Set<WebRtpErrorCallback>();
  private readonly eventListeners = new Set<WebRtpEventCallback>();

  constructor(wsUrl: string, options: WebRtpClientOptions = {}) {
    this.wsUrl = wsUrl;
    this.options = {
      autoReconnect: options.autoReconnect ?? true,
      reconnectDelayMs: options.reconnectDelayMs ?? 1000,
      maxReconnectDelayMs: options.maxReconnectDelayMs ?? 5000,
      lateFrameThreshold: options.lateFrameThreshold ?? 16,
      maxPendingDecode: options.maxPendingDecode ?? 8,
    };
    this.reconnectDelay = this.options.reconnectDelayMs;
    this.connect();
  }

  private connect(): void {
    if (this.closed) {
      return;
    }
    this.emitEvent({ type: 'connecting', wsUrl: this.wsUrl });
    const ws = new WebSocket(this.wsUrl);
    this.ws = ws;
    ws.binaryType = 'arraybuffer';
    ws.onopen = (event) => {
      if (this.ws !== ws || this.closed) {
        return;
      }
      this.reconnectDelay = this.options.reconnectDelayMs;
      this.emitEvent({ type: 'open', wsUrl: this.wsUrl, event });
    };
    ws.onmessage = (event) => {
      if (this.ws !== ws || this.closed) {
        return;
      }
      this.onMessage(event);
    };
    ws.onclose = (event) => {
      if (this.ws !== ws) {
        return;
      }
      this.ws = null;
      this.emitEvent({ type: 'close', wsUrl: this.wsUrl, event });
      this.scheduleReconnect();
    };
    ws.onerror = (event) => {
      if (this.ws !== ws || this.closed) {
        return;
      }
      this.emitEvent({ type: 'error', wsUrl: this.wsUrl, event });
    };
  }

  private scheduleReconnect(): void {
    if (this.closed || !this.options.autoReconnect) {
      return;
    }
    this.resetStreamState();
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
    }
    this.emitEvent({
      type: 'reconnect-scheduled',
      wsUrl: this.wsUrl,
      reconnectDelayMs: this.reconnectDelay,
    });
    this.reconnectTimer = window.setTimeout(() => {
      this.connect();
    }, this.reconnectDelay);
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.options.maxReconnectDelayMs);
  }

  private resetStreamState(): void {
    this.initDone = false;
    this.codec = null;
    this.description = null;
    this.avcParameterSets = [];
    this.avcDisplayRect = null;
    this.firstFrame = true;
    this.frameIndex = 0;
    this.lastSourceFrameNo = 0;
    this.pendingDecode = 0;
    this.waitingForKeyframe = false;
    this.decodedPictureCount = 0;
    if (this.decoderWatchdogTimer !== null) {
      window.clearTimeout(this.decoderWatchdogTimer);
      this.decoderWatchdogTimer = null;
    }
    if (this.decoder && this.decoder.state !== 'closed') {
      try {
        this.decoder.close();
      } catch {
        // ignore close failures during reset
      }
    }
    this.decoder = null;
  }

  private onMessage(event: MessageEvent): void {
    if (this.paused) {
      return;
    }
    if (!(event.data instanceof ArrayBuffer)) {
      return;
    }

    if (!this.initDone) {
      this.initDone = true;
      const parsed = this.parseInit(event.data);
      this.codec = parsed.codec;
      this.description = parsed.description;
      this.avcParameterSets = parsed.avcParameterSets;
      this.avcDisplayRect = this.parseAvcDisplayRect(this.avcParameterSets);
      this.emitInfo({
        codec: this.codec,
        frameNo: 0,
        dropped: this.dropped,
        paused: this.paused,
      });
      this.makeDecoder(this.codec, this.description);
      return;
    }

    if (!this.decoder) {
      return;
    }

    const { frameNo, payload, isKey } = this.parseFrag(event.data);
    this.lastSourceFrameNo = frameNo;
    const decodeQueue = this.decoder ? Math.max(this.decoder.decodeQueueSize || 0, this.pendingDecode) : this.pendingDecode;

    if (this.waitingForKeyframe && !isKey) {
      this.dropped++;
      return;
    }
    if (decodeQueue >= this.options.lateFrameThreshold && !isKey) {
      this.dropped++;
      this.waitingForKeyframe = true;
      return;
    }
    if (decodeQueue >= this.options.maxPendingDecode && !isKey) {
      this.dropped++;
      return;
    }

    const timestamp = (++this.frameIndex) * 100_000;
    this.emitFrame(frameNo, payload, isKey);

    try {
      this.pendingDecode++;
      if (this.decoder) {
        this.decoder.decode(
          new EncodedVideoChunk({
            type: isKey ? 'key' : 'delta',
            timestamp,
            data: payload,
          }),
        );
      }
      if (isKey) {
        this.waitingForKeyframe = false;
      }
      if (this.firstFrame && isKey) {
        this.firstFrame = false;
        this.emitInfo({ playing: true, frameNo });
      }
    } catch (error) {
      this.dropped++;
      this.pendingDecode = Math.max(0, this.pendingDecode - 1);
      this.emitError(error instanceof Error ? error : new Error(String(error)));
      if (this.decoder && this.decoder.state !== 'configured' && this.codec) {
        this.makeDecoder(this.codec, this.description);
      }
    }
  }

  private parseInit(buf: ArrayBuffer): { codec: string; description: ArrayBuffer | null; avcParameterSets: Uint8Array[] } {
    const bytes = new Uint8Array(buf);
    for (let i = 0; i < bytes.length - 8; i++) {
      if (bytes[i] === 0x61 && bytes[i + 1] === 0x76 && bytes[i + 2] === 0x63 && bytes[i + 3] === 0x43) {
        const payloadStart = i + 4;
        const boxSize =
          ((bytes[i - 4] << 24) | (bytes[i - 3] << 16) | (bytes[i - 2] << 8) | bytes[i - 1]) >>> 0;
        const payloadLength = boxSize - 8;
        const description = buf.slice(payloadStart, payloadStart + payloadLength);
        const profile = bytes[payloadStart + 1].toString(16).padStart(2, '0');
        const compat = bytes[payloadStart + 2].toString(16).padStart(2, '0');
        const level = bytes[payloadStart + 3].toString(16).padStart(2, '0');
        return {
          codec: `avc1.${profile}${compat}${level}`,
          description,
          avcParameterSets: this.parseAvcParameterSets(description),
        };
      }
      if (bytes[i] === 0x68 && bytes[i + 1] === 0x76 && bytes[i + 2] === 0x63 && bytes[i + 3] === 0x43) {
        const payloadStart = i + 4;
        const boxSize =
          ((bytes[i - 4] << 24) | (bytes[i - 3] << 16) | (bytes[i - 2] << 8) | bytes[i - 1]) >>> 0;
        const generalProfile = bytes[payloadStart + 1];
        const generalTierFlag = (bytes[payloadStart + 2] >> 7) & 1;
        const generalLevel = bytes[payloadStart + 6];
        return {
          codec: `hvc1.${generalProfile}.${generalLevel}.${generalTierFlag === 0 ? 'L' : 'H'}`,
          description: buf.slice(payloadStart, payloadStart + boxSize - 8),
          avcParameterSets: [],
        };
      }
    }
    return { codec: 'avc1.640028', description: null, avcParameterSets: [] };
  }

  private parseAvcParameterSets(description: ArrayBuffer): Uint8Array[] {
    const bytes = new Uint8Array(description);
    if (bytes.length < 7 || bytes[0] !== 1) {
      return [];
    }
    let offset = 5;
    const sets: Uint8Array[] = [];
    const spsCount = bytes[offset++] & 0x1f;
    for (let i = 0; i < spsCount && offset + 2 <= bytes.length; i++) {
      const length = (bytes[offset] << 8) | bytes[offset + 1];
      offset += 2;
      if (offset + length > bytes.length) {
        return sets;
      }
      sets.push(bytes.slice(offset, offset + length));
      offset += length;
    }
    if (offset >= bytes.length) {
      return sets;
    }
    const ppsCount = bytes[offset++];
    for (let i = 0; i < ppsCount && offset + 2 <= bytes.length; i++) {
      const length = (bytes[offset] << 8) | bytes[offset + 1];
      offset += 2;
      if (offset + length > bytes.length) {
        return sets;
      }
      sets.push(bytes.slice(offset, offset + length));
      offset += length;
    }
    return sets;
  }

  private parseFrag(buf: ArrayBuffer): { frameNo: number; payload: Uint8Array; isKey: boolean } {
    const bytes = new Uint8Array(buf);
    const frameNo = Number(new DataView(buf).getBigUint64(0));
    const data = buf.slice(8);

    for (let i = 0; i < bytes.length - 16; i++) {
      if (
        bytes[i + 12] === 0x6d &&
        bytes[i + 13] === 0x64 &&
        bytes[i + 14] === 0x61 &&
        bytes[i + 15] === 0x74
      ) {
        const size = ((bytes[i + 8] << 24) | (bytes[i + 9] << 16) | (bytes[i + 10] << 8) | bytes[i + 11]) >>> 0;
        const payload = new Uint8Array(data.slice(i + 8, i + size));
        let isKey = false;
        let position = 0;
        while (position + 4 < payload.length) {
          const nalLength =
            ((payload[position] << 24) |
              (payload[position + 1] << 16) |
              (payload[position + 2] << 8) |
              payload[position + 3]) >>>
            0;
          if (nalLength === 0 || position + 4 + nalLength > payload.length) {
            break;
          }
          const nalType = payload[position + 4] & 0x1f;
          if (nalType === 5 || nalType === 19) {
            isKey = true;
            break;
          }
          if (nalType === 1) {
            break;
          }
          position += 4 + nalLength;
        }
        return { frameNo, payload, isKey };
      }
    }

    return { frameNo, payload: new Uint8Array(data), isKey: false };
  }

  private makeDecoder(codec: string, description: ArrayBuffer | null): void {
    if (typeof VideoDecoder === 'undefined') {
      const message = window.isSecureContext
        ? 'WebCodecs VideoDecoder is not available in this browser.'
        : 'WebCodecs VideoDecoder requires a secure context. Open this page via https:// or http://localhost.';
      const error = new Error(message);
      this.emitError(error);
      throw error;
    }

    if (this.decoder && this.decoder.state !== 'closed') {
      try {
        this.decoder.close();
      } catch {
        // ignore close failures during reconfigure
      }
    }
    this.startDecoderWatchdog('webcodecs', codec);
    this.emitEvent({ type: 'decoder-selected', wsUrl: this.wsUrl, decoder: 'webcodecs', codec });

    try {
      this.decoder = new VideoDecoder({
        output: (frame) => {
          this.pendingDecode = Math.max(0, this.pendingDecode - 1);
          this.markPictureDecoded();
          const width = frame.displayWidth;
          const height = frame.displayHeight;
          if (this.canvas && this.ctx) {
            if (this.canvas.width !== width || this.canvas.height !== height) {
              this.canvas.width = width;
              this.canvas.height = height;
            }
            this.ctx.drawImage(frame, 0, 0, width, height);
          }
          this.emitInfo({
            codec: this.codec ?? undefined,
            frameNo: this.lastSourceFrameNo,
            width,
            height,
            dropped: this.dropped,
            paused: this.paused,
          });
          frame.close();
        },
        error: (error) => {
          this.pendingDecode = Math.max(0, this.pendingDecode - 1);
          this.waitingForKeyframe = true;
          this.dropped++;
          this.emitError(error instanceof Error ? error : new Error(String(error)));
        },
      });

      const config: VideoDecoderConfig = {
        codec,
        optimizeForLatency: true,
        hardwareAcceleration: 'prefer-hardware',
      };
      if (description) {
        config.description = description;
      }
      this.decoder.configure(config);
    } catch (error) {
      this.decoder = null;
      const nextError = error instanceof Error ? error : new Error(String(error));
      this.emitError(nextError);
      throw nextError;
    }
  }

  private startDecoderWatchdog(decoder: 'webcodecs', codec: string): void {
    if (this.decoderWatchdogTimer !== null) {
      window.clearTimeout(this.decoderWatchdogTimer);
    }
    const expectedDecodedPictures = this.decodedPictureCount;
    this.decoderWatchdogTimer = window.setTimeout(() => {
      if (this.closed || this.decodedPictureCount > expectedDecodedPictures) {
        return;
      }
      this.emitEvent({
        type: 'decoder-stalled',
        wsUrl: this.wsUrl,
        decoder,
        codec,
      });
      this.emitError(new Error(`Decoder "${decoder}" selected for codec "${codec}" but no decoded picture was produced.`));
    }, 3000);
  }

  private markPictureDecoded(): void {
    this.decodedPictureCount++;
    if (this.decoderWatchdogTimer !== null) {
      window.clearTimeout(this.decoderWatchdogTimer);
      this.decoderWatchdogTimer = null;
    }
  }

  private parseAvcDisplayRect(parameterSets: Uint8Array[]): AvcDisplayRect | null {
    const sps = parameterSets.find((unit) => unit.length > 0 && (unit[0] & 0x1f) === 7);
    if (!sps || sps.length < 4) {
      return null;
    }
    const rbsp = this.naluToRbsp(sps.subarray(1));
    const bits = new ExpGolombReader(rbsp);
    try {
      const profileIdc = bits.readBits(8);
      bits.readBits(8);
      bits.readBits(8);
      bits.readUE();

      let chromaFormatIdc = 1;
      let separateColorPlaneFlag = 0;
      if (
        profileIdc === 100 ||
        profileIdc === 110 ||
        profileIdc === 122 ||
        profileIdc === 244 ||
        profileIdc === 44 ||
        profileIdc === 83 ||
        profileIdc === 86 ||
        profileIdc === 118 ||
        profileIdc === 128 ||
        profileIdc === 138 ||
        profileIdc === 139 ||
        profileIdc === 134 ||
        profileIdc === 135
      ) {
        chromaFormatIdc = bits.readUE();
        if (chromaFormatIdc === 3) {
          separateColorPlaneFlag = bits.readBits(1);
        }
        bits.readUE();
        bits.readUE();
        bits.readBits(1);
        if (bits.readBool()) {
          const scalingListCount = chromaFormatIdc !== 3 ? 8 : 12;
          for (let i = 0; i < scalingListCount; i++) {
            if (bits.readBool()) {
              this.skipScalingList(bits, i < 6 ? 16 : 64);
            }
          }
        }
      }

      bits.readUE();
      const picOrderCntType = bits.readUE();
      if (picOrderCntType === 0) {
        bits.readUE();
      } else if (picOrderCntType === 1) {
        bits.readBits(1);
        bits.readSE();
        bits.readSE();
        const cycleLength = bits.readUE();
        for (let i = 0; i < cycleLength; i++) {
          bits.readSE();
        }
      }

      bits.readUE();
      bits.readBits(1);
      const picWidthInMbsMinus1 = bits.readUE();
      const picHeightInMapUnitsMinus1 = bits.readUE();
      const frameMbsOnlyFlag = bits.readBits(1);
      if (frameMbsOnlyFlag === 0) {
        bits.readBits(1);
      }
      bits.readBits(1);

      let frameCropLeftOffset = 0;
      let frameCropRightOffset = 0;
      let frameCropTopOffset = 0;
      let frameCropBottomOffset = 0;
      if (bits.readBool()) {
        frameCropLeftOffset = bits.readUE();
        frameCropRightOffset = bits.readUE();
        frameCropTopOffset = bits.readUE();
        frameCropBottomOffset = bits.readUE();
      }

      const codedWidth = (picWidthInMbsMinus1 + 1) * 16;
      const codedHeight = (2 - frameMbsOnlyFlag) * (picHeightInMapUnitsMinus1 + 1) * 16;

      let cropUnitX = 1;
      let cropUnitY = 2 - frameMbsOnlyFlag;
      if (separateColorPlaneFlag === 0) {
        if (chromaFormatIdc === 1) {
          cropUnitX = 2;
          cropUnitY = 2 * (2 - frameMbsOnlyFlag);
        } else if (chromaFormatIdc === 2) {
          cropUnitX = 2;
          cropUnitY = 2 - frameMbsOnlyFlag;
        }
      }

      const cropLeft = frameCropLeftOffset * cropUnitX;
      const cropRight = frameCropRightOffset * cropUnitX;
      const cropTop = frameCropTopOffset * cropUnitY;
      const cropBottom = frameCropBottomOffset * cropUnitY;

      return {
        codedWidth,
        codedHeight,
        width: codedWidth - cropLeft - cropRight,
        height: codedHeight - cropTop - cropBottom,
        cropLeft,
        cropTop,
      };
    } catch {
      return null;
    }
  }

  private naluToRbsp(nalu: Uint8Array): Uint8Array {
    const rbsp: number[] = [];
    let zeroCount = 0;
    for (const byte of nalu) {
      if (zeroCount === 2 && byte === 0x03) {
        zeroCount = 0;
        continue;
      }
      rbsp.push(byte);
      if (byte === 0) {
        zeroCount++;
      } else {
        zeroCount = 0;
      }
    }
    return Uint8Array.from(rbsp);
  }

  private skipScalingList(bits: ExpGolombReader, count: number): void {
    let lastScale = 8;
    let nextScale = 8;
    for (let i = 0; i < count; i++) {
      if (nextScale !== 0) {
        const deltaScale = bits.readSE();
        nextScale = (lastScale + deltaScale + 256) % 256;
      }
      lastScale = nextScale === 0 ? lastScale : nextScale;
    }
  }

  render(target: HTMLCanvasElement | HTMLVideoElement | HTMLElement): this {
    this.detach();
    if (target instanceof HTMLCanvasElement) {
      this.canvas = target;
      this.videoEl = null;
      this.canvasHost = null;
      this.canvasStream = null;
    } else if (target instanceof HTMLVideoElement) {
      this.videoEl = target;
      if (!this.canvas) {
        this.canvas = document.createElement('canvas');
      }
      this.canvasHost = null;
      this.attachVideoStream();
    } else {
      this.videoEl = null;
      if (!this.canvas) {
        this.canvas = document.createElement('canvas');
      }
      this.canvasHost = target;
      if (this.canvas.parentNode !== target) {
        target.appendChild(this.canvas);
      }
    }
    this.ctx = this.canvas?.getContext('2d', { alpha: false }) ?? null;
    return this;
  }

  private attachVideoStream(): void {
    if (!this.videoEl || !this.canvas) {
      return;
    }
    if (!this.canvasStream) {
      this.canvasStream = this.canvas.captureStream();
    }
    this.videoEl.srcObject = this.canvasStream;
    void this.videoEl.play().catch(() => {
      // autoplay may be blocked until user interaction
    });
  }

  info(): WebRtpInfo {
    return {
      codec: this.codec ?? undefined,
      frameNo: this.lastSourceFrameNo,
      dropped: this.dropped,
      paused: this.paused,
    };
  }

  getCanvas(): HTMLCanvasElement | null {
    return this.canvas;
  }

  play(): void {
    this.paused = false;
    if (this.videoEl) {
      void this.videoEl.play().catch(() => {
        // autoplay may be blocked until user interaction
      });
    }
  }

  pause(): void {
    this.paused = true;
    this.videoEl?.pause();
  }

  private emitInfo(info: WebRtpInfo): void {
    for (const listener of this.infoListeners) {
      listener(info);
    }
  }

  private emitFrame(frameNo: number, data: Uint8Array, isKey: boolean): void {
    for (const listener of this.frameListeners) {
      listener(frameNo, data, isKey);
    }
  }

  private emitError(error: Error): void {
    for (const listener of this.errorListeners) {
      listener(error);
    }
  }

  private emitEvent(event: WebRtpEvent): void {
    for (const listener of this.eventListeners) {
      listener(event);
    }
  }

  onInfo(callback: WebRtpInfoCallback): () => void {
    this.infoListeners.add(callback);
    return () => {
      this.infoListeners.delete(callback);
    };
  }

  onFrame(callback: WebRtpFrameCallback): () => void {
    this.frameListeners.add(callback);
    return () => {
      this.frameListeners.delete(callback);
    };
  }

  onError(callback: WebRtpErrorCallback): () => void {
    this.errorListeners.add(callback);
    return () => {
      this.errorListeners.delete(callback);
    };
  }

  onEvent(callback: WebRtpEventCallback): () => void {
    this.eventListeners.add(callback);
    return () => {
      this.eventListeners.delete(callback);
    };
  }

  detach(): void {
    if (this.videoEl) {
      this.videoEl.pause();
      this.videoEl.srcObject = null;
    }
    if (this.canvasHost && this.canvas && this.canvas.parentNode === this.canvasHost) {
      this.canvasHost.removeChild(this.canvas);
    }
    this.videoEl = null;
    this.canvasHost = null;
  }

  close(): void {
    this.closed = true;
    this.paused = true;
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.decoderWatchdogTimer !== null) {
      window.clearTimeout(this.decoderWatchdogTimer);
      this.decoderWatchdogTimer = null;
    }
    this.resetStreamState();
    this.detach();
    if (this.canvasStream) {
      for (const track of this.canvasStream.getTracks()) {
        track.stop();
      }
      this.canvasStream = null;
    }
    this.canvas = null;
    this.ctx = null;
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      this.ws.close();
    }
    this.ws = null;
  }
}

export function createClient(wsUrl: string, options?: WebRtpClientOptions): WebRtpClient {
  return new WebRtpClient(wsUrl, options);
}

class ExpGolombReader {
  private byteOffset = 0;
  private bitOffset = 0;

  constructor(private readonly bytes: Uint8Array) {}

  readBits(count: number): number {
    let value = 0;
    for (let i = 0; i < count; i++) {
      if (this.byteOffset >= this.bytes.length) {
        throw new Error('Unexpected end of SPS.');
      }
      value <<= 1;
      value |= (this.bytes[this.byteOffset] >> (7 - this.bitOffset)) & 1;
      this.bitOffset++;
      if (this.bitOffset === 8) {
        this.bitOffset = 0;
        this.byteOffset++;
      }
    }
    return value;
  }

  readBool(): boolean {
    return this.readBits(1) === 1;
  }

  readUE(): number {
    let leadingZeroBits = 0;
    while (this.readBits(1) === 0) {
      leadingZeroBits++;
    }
    if (leadingZeroBits === 0) {
      return 0;
    }
    return (1 << leadingZeroBits) - 1 + this.readBits(leadingZeroBits);
  }

  readSE(): number {
    const codeNum = this.readUE();
    const value = Math.ceil(codeNum / 2);
    return codeNum % 2 === 0 ? -value : value;
  }
}
