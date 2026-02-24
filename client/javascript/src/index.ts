export interface WebRtpInfo {
    playing?: boolean;
    frameNo?: number;
    codec?: string;
    width?: number;
    height?: number;
    dropped?: number;
}

export type WebRtpInfoCallback = (info: WebRtpInfo) => void;
export type WebRtpFrameCallback = (frameNo: number, data: Uint8Array, isKey: boolean) => void;

export class WebRtpClient {
    private ws: WebSocket;
    private canvas: HTMLCanvasElement | null = null;
    private ctx: CanvasRenderingContext2D | null = null;
    private decoder: VideoDecoder | null = null;
    private codec: string | null = null;
    private description: ArrayBuffer | null = null;
    private frameIndex = 0;
    private initDone = false;
    private firstFrame = true;
    private dropped = 0;
    private _oninfo: WebRtpInfoCallback | null = null;
    private _onframe: WebRtpFrameCallback | null = null;
    private _paused = false;

    constructor(wsUrl: string) {
        this.ws = new WebSocket(wsUrl);
        this.ws.binaryType = 'arraybuffer';
        this.ws.onmessage = (e) => this.onMessage(e);
    }

    private onMessage(e: MessageEvent): void {
        if (this._paused) return;

        if (!this.initDone) {
            this.initDone = true;
            const parsed = this.parseInit(e.data);
            this.codec = parsed.codec;
            this.description = parsed.description;
            this.makeDecoder(this.codec, this.description);
            return;
        }

        if (!this.decoder || this.decoder.state !== 'configured') return;

        const { frameNo, payload, isKey } = this.parseFrag(e.data);
        const ts = (++this.frameIndex) * 100_000;

        if (this._onframe) {
            this._onframe(frameNo, payload, isKey);
        }

        try {
            this.decoder.decode(new EncodedVideoChunk({
                type: isKey ? 'key' : 'delta',
                timestamp: ts,
                data: payload
            }));
            if (this.firstFrame && isKey) {
                this.firstFrame = false;
                if (this._oninfo) this._oninfo({ playing: true, frameNo });
            }
        } catch (err) {
            this.dropped++;
            if (this.decoder.state !== 'configured') {
                this.makeDecoder(this.codec!, this.description);
            }
        }
    }

    private parseInit(buf: ArrayBuffer): { codec: string; description: ArrayBuffer | null } {
        const b = new Uint8Array(buf);
        for (let i = 0; i < b.length - 8; i++) {
            // avcC (H.264)
            if (b[i] === 0x61 && b[i + 1] === 0x76 && b[i + 2] === 0x63 && b[i + 3] === 0x43) {
                const payStart = i + 4;
                const boxSize = ((b[i - 4] << 24) | (b[i - 3] << 16) | (b[i - 2] << 8) | b[i - 1]) >>> 0;
                const payLen = boxSize - 8;
                const profile = b[payStart + 1].toString(16).padStart(2, '0');
                const compat = b[payStart + 2].toString(16).padStart(2, '0');
                const level = b[payStart + 3].toString(16).padStart(2, '0');
                return {
                    codec: 'avc1.' + profile + compat + level,
                    description: buf.slice(payStart, payStart + payLen)
                };
            }
            // hvcC (H.265/HEVC)
            if (b[i] === 0x68 && b[i + 1] === 0x76 && b[i + 2] === 0x63 && b[i + 3] === 0x43) {
                const payStart = i + 4;
                const boxSize = ((b[i - 4] << 24) | (b[i - 3] << 16) | (b[i - 2] << 8) | b[i - 1]) >>> 0;
                const generalProfileSpace = b[payStart];
                const generalProfile = b[payStart + 1];
                const generalTierFlag = (b[payStart + 2] >> 7) & 1;
                const generalProfileCompatibility = ((b[payStart + 2] & 0x7F) << 24) | (b[payStart + 3] << 16) | (b[payStart + 4] << 8) | b[payStart + 5];
                const generalLevel = b[payStart + 6];
                const codecStr = `hvc1.${generalProfile}.${generalLevel}.${generalTierFlag === 0 ? 'L' : 'H'}`;
                return { codec: codecStr, description: buf.slice(payStart, payStart + boxSize - 8) };
            }
        }
        return { codec: 'avc1.640028', description: null };
    }

    private parseFrag(buf: ArrayBuffer): { frameNo: number; payload: Uint8Array; isKey: boolean } {
        const b = new Uint8Array(buf);
        const frameNo = Number(new DataView(buf).getBigUint64(0));
        const data = buf.slice(8);

        for (let i = 0; i < b.length - 8 - 8; i++) {
            if (b[i + 4 + 8] === 0x6D && b[i + 5 + 8] === 0x64 && b[i + 6 + 8] === 0x61 && b[i + 7 + 8] === 0x74) {
                const size = ((b[i + 8] << 24) | (b[i + 1 + 8] << 16) | (b[i + 2 + 8] << 8) | b[i + 3 + 8]) >>> 0;
                const payload = new Uint8Array(data.slice(i + 8, i + size));
                let isKey = false, pos = 0;
                while (pos + 4 < payload.length) {
                    const nl = ((payload[pos] << 24) | (payload[pos + 1] << 16) | (payload[pos + 2] << 8) | payload[pos + 3]) >>> 0;
                    if (nl === 0 || pos + 4 + nl > payload.length) break;
                    const t = payload[pos + 4] & 0x1F;
                    if (t === 5 || t === 19) {
                        isKey = true;
                        break;
                    }
                    if (t === 1) break;
                    pos += 4 + nl;
                }
                return { frameNo, payload, isKey };
            }
        }
        return { frameNo, payload: new Uint8Array(data), isKey: false };
    }

    private makeDecoder(codec: string, description: ArrayBuffer | null): void {
        if (this.decoder && this.decoder.state !== 'closed') {
            try {
                this.decoder.close();
            } catch (_) { }
        }
        this.decoder = new VideoDecoder({
            output: (frame) => {
                const w = frame.displayWidth, h = frame.displayHeight;
                if (this.canvas && this.ctx) {
                    if (this.canvas.width !== w || this.canvas.height !== h) {
                        this.canvas.width = w;
                        this.canvas.height = h;
                    }
                    this.ctx.drawImage(frame, 0, 0, w, h);
                }
                frame.close();
            },
            error: (e) => {
                this.dropped++;
            },
        });
        const cfg: VideoDecoderConfig = { codec, optimizeForLatency: true, hardwareAcceleration: 'prefer-hardware' };
        if (description) cfg.description = description;
        this.decoder.configure(cfg);
    }

    render(target: HTMLCanvasElement | HTMLElement): this {
        if (target instanceof HTMLCanvasElement) {
            this.canvas = target;
        } else {
            this.canvas = document.createElement('canvas');
            target.appendChild(this.canvas);
        }
        this.ctx = this.canvas.getContext('2d', { alpha: false });
        return this;
    }

    info(): WebRtpInfo {
        return {
            codec: this.codec ?? undefined,
            frameNo: this.frameIndex,
            dropped: this.dropped,
        };
    }

    play(): void {
        this._paused = false;
    }

    pause(): void {
        this._paused = true;
    }

    onInfo(fn: WebRtpInfoCallback): void {
        this._oninfo = fn;
    }

    onFrame(fn: WebRtpFrameCallback): void {
        this._onframe = fn;
    }

    close(): void {
        this._paused = true;
        if (this.decoder && this.decoder.state !== 'closed') {
            try {
                this.decoder.close();
            } catch (_) { }
        }
        if (this.ws.readyState === WebSocket.OPEN) {
            this.ws.close();
        }
    }
}

export function createClient(wsUrl: string): WebRtpClient {
    return new WebRtpClient(wsUrl);
}
