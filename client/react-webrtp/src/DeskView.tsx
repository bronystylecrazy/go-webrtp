import React, { forwardRef, useCallback, useEffect, useEffectEvent, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from 'react';
import {
  type WebRtpClient,
  type WebRtpClientOptions,
  type WebRtpErrorCallback,
  type WebRtpEventCallback,
  type WebRtpFrameCallback,
  type WebRtpInfoCallback,
} from './client';
import { drawImageQuadMesh, drawWarpMesh, type Rect } from './canvasMesh';
import { clamp01, computeDeskOutputSize, displayToSourceTexture, sourceToDisplayTexture } from './deskTransform';
import { WebGlEffectRenderer } from './effectRenderer';
import { useWebRtpStream } from './WebRtpProvider';

export interface DeskViewPoint {
  x: number;
  y: number;
  label?: string;
}

export interface DeskViewHandle {
  getClient(): WebRtpClient | null;
  getCanvas(): HTMLCanvasElement | null;
  redraw(): void;
}

export interface DeskViewProps
  extends Omit<React.HTMLAttributes<HTMLDivElement>, 'onError'>,
    WebRtpClientOptions {
  url: string;
  points: DeskViewPoint[];
  fx?: number;
  fy?: number;
  dy?: number;
  scale?: number;
  backgroundColor?: string;
  meshSteps?: number;
  onInfo?: WebRtpInfoCallback;
  onFrame?: WebRtpFrameCallback;
  onEvent?: WebRtpEventCallback;
  onPlayerError?: WebRtpErrorCallback;
  canvasProps?: React.CanvasHTMLAttributes<HTMLCanvasElement>;
}

const DEFAULT_POINTS: DeskViewPoint[] = [
  { x: 0.2, y: 0.2, label: 'A' },
  { x: 0.8, y: 0.24, label: 'B' },
  { x: 0.88, y: 0.82, label: 'C' },
  { x: 0.12, y: 0.78, label: 'D' },
];

const DEFAULT_BACKGROUND = '#020617';

export const DeskView = forwardRef<DeskViewHandle, DeskViewProps>(function DeskView(
  {
    url,
    points,
    fx = 0,
    fy,
    dy,
    scale = 1,
    backgroundColor = DEFAULT_BACKGROUND,
    meshSteps = 18,
    onInfo,
    onFrame,
    onEvent,
    onPlayerError,
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
    canvasProps,
    style,
    ...divProps
  },
  ref,
) {
  const stream = useWebRtpStream(url, {
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
  });
  const containerRef = useRef<HTMLDivElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const sourceCanvasRef = useRef<HTMLCanvasElement | null>(null);
  const correctedCanvasRef = useRef<HTMLCanvasElement | null>(null);
  const correctedRendererRef = useRef<WebGlEffectRenderer | null>(null);
  const clientRef = useRef<WebRtpClient | null>(null);
  const frameRequestRef = useRef<number | null>(null);
  const resizeObserverRef = useRef<ResizeObserver | null>(null);
  const renderDeskViewRef = useRef<() => void>(() => {});
  const [sourceSize, setSourceSize] = useState({ width: 1, height: 1 });

  const normalizedPoints = useMemo(() => normalizeDeskPoints(points), [points]);
  const pointSignature = useMemo(
    () => normalizedPoints.map((point) => `${point.x.toFixed(4)},${point.y.toFixed(4)}`).join(';'),
    [normalizedPoints],
  );
  const normalizedFy = fy ?? dy ?? 0;
  const deskOutputSize = useMemo(
    () => computeDeskOutputSize(sourceSize.width, sourceSize.height, normalizedPoints, fx, normalizedFy, scale),
    [sourceSize.width, sourceSize.height, normalizedPoints, fx, normalizedFy, scale],
  );
  const deskAspectRatio = Math.max(0.001, deskOutputSize.width / deskOutputSize.height);

  const handleInfo = useEffectEvent((nextInfo: Parameters<WebRtpInfoCallback>[0]) => {
    onInfo?.(nextInfo);
  });
  const handleFrame = useEffectEvent((...args: Parameters<WebRtpFrameCallback>) => {
    onFrame?.(...args);
  });
  const handleEvent = useEffectEvent((nextEvent: Parameters<WebRtpEventCallback>[0]) => {
    onEvent?.(nextEvent);
  });
  const handlePlayerError = useEffectEvent((nextError: Parameters<WebRtpErrorCallback>[0]) => {
    onPlayerError?.(nextError);
  });

  const renderDeskView = useCallback(() => {
    const canvas = canvasRef.current;
    const sourceCanvas = sourceCanvasRef.current;
    const container = containerRef.current;
    if (!canvas || !sourceCanvas || !container) {
      return;
    }

    const sourceWidth = Number(sourceCanvas.width || 0);
    const sourceHeight = Number(sourceCanvas.height || 0);
    if (sourceWidth <= 0 || sourceHeight <= 0) {
      return;
    }
    setSourceSize((current) =>
      current.width === sourceWidth && current.height === sourceHeight
        ? current
        : { width: sourceWidth, height: sourceHeight },
    );

    const cssWidth = Math.max(1, Math.round(container.clientWidth || sourceWidth));
    const cssHeight = Math.max(1, Math.round(cssWidth / deskAspectRatio));
    const dpr = window.devicePixelRatio || 1;
    const backingWidth = Math.max(1, Math.round(cssWidth * dpr));
    const backingHeight = Math.max(1, Math.round(cssHeight * dpr));

    if (canvas.width !== backingWidth || canvas.height !== backingHeight) {
      canvas.width = backingWidth;
      canvas.height = backingHeight;
    }

    const ctx = canvas.getContext('2d');
    if (!ctx) {
      return;
    }

    const correctedCanvas = getCorrectedCanvas(
      sourceCanvas,
      correctedCanvasRef,
      correctedRendererRef,
      fx,
      normalizedFy,
      scale,
      meshSteps,
    );
    const textureWidth = correctedCanvas.width;
    const textureHeight = correctedCanvas.height;
    if (textureWidth <= 0 || textureHeight <= 0) {
      return;
    }

    const outputSize = computeDeskOutputSize(textureWidth, textureHeight, normalizedPoints, fx, normalizedFy, scale);
    const drawRect = fitRect(cssWidth, cssHeight, outputSize.width, outputSize.height);
    const srcQuad = normalizedPoints.map((point) => {
      const mapped = sourceToDisplayTexture(point, fx, normalizedFy, scale);
      return {
        x: mapped.x * textureWidth,
        y: mapped.y * textureHeight,
      };
    });

    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, backingWidth, backingHeight);
    ctx.fillStyle = backgroundColor;
    ctx.fillRect(0, 0, backingWidth, backingHeight);
    ctx.scale(dpr, dpr);
    drawImageQuadMesh(ctx, correctedCanvas, srcQuad, drawRect, meshSteps, meshSteps);
  }, [backgroundColor, deskAspectRatio, fx, meshSteps, normalizedFy, normalizedPoints, scale]);

  const scheduleRender = useCallback(() => {
    if (frameRequestRef.current !== null) {
      return;
    }
    frameRequestRef.current = window.requestAnimationFrame(() => {
      frameRequestRef.current = null;
      renderDeskViewRef.current();
    });
  }, []);

  const redrawNow = useCallback(() => {
    if (frameRequestRef.current !== null) {
      window.cancelAnimationFrame(frameRequestRef.current);
      frameRequestRef.current = null;
    }
    renderDeskView();
  }, [renderDeskView]);

  useLayoutEffect(() => {
    renderDeskViewRef.current = renderDeskView;
  }, [renderDeskView]);

  useImperativeHandle(
    ref,
    () => ({
      getClient: () => clientRef.current,
      getCanvas: () => canvasRef.current,
      redraw: () => {
        redrawNow();
      },
    }),
    [redrawNow],
  );

  useEffect(() => {
    const client = stream.client;
    if (!client) {
      return;
    }
    clientRef.current = client;
    const sourceCanvas = client.getCanvas() ?? document.createElement('canvas');
    sourceCanvasRef.current = sourceCanvas;
    if (!client.getCanvas()) {
      client.render(sourceCanvas);
    }

    const offInfo = client.onInfo((nextInfo) => {
      handleInfo(nextInfo);
      sourceCanvasRef.current = client.getCanvas() ?? sourceCanvasRef.current;
      scheduleRender();
    });
    const offFrame = client.onFrame((frameNo, data, isKey) => {
      handleFrame(frameNo, data, isKey);
      sourceCanvasRef.current = client.getCanvas() ?? sourceCanvasRef.current;
      scheduleRender();
    });
    const offEvent = client.onEvent((nextEvent) => {
      handleEvent(nextEvent);
    });
    const offError = client.onError((nextError) => {
      handlePlayerError(nextError);
    });
    client.play();

    return () => {
      offInfo();
      offFrame();
      offEvent();
      offError();
      if (frameRequestRef.current !== null) {
        window.cancelAnimationFrame(frameRequestRef.current);
        frameRequestRef.current = null;
      }
      if (clientRef.current === client) {
        clientRef.current = null;
      }
      sourceCanvasRef.current = null;
      correctedRendererRef.current = null;
    };
  }, [
    stream.client,
  ]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    resizeObserverRef.current?.disconnect();
    const observer = new ResizeObserver(() => {
      scheduleRender();
    });
    observer.observe(container);
    resizeObserverRef.current = observer;
    return () => {
      observer.disconnect();
      if (resizeObserverRef.current === observer) {
        resizeObserverRef.current = null;
      }
    };
  }, []);

  useLayoutEffect(() => {
    redrawNow();
  }, [pointSignature, fx, normalizedFy, scale, backgroundColor, meshSteps, redrawNow]);

  return (
    <div
      {...divProps}
      ref={containerRef}
      style={{
        position: 'relative',
        width: '100%',
        aspectRatio: String(deskAspectRatio),
        ...style,
      }}
    >
      <canvas
        {...canvasProps}
        ref={canvasRef}
        style={{
          display: 'block',
          width: '100%',
          height: '100%',
          ...(canvasProps?.style ?? null),
        }}
      />
    </div>
  );
});

function normalizeDeskPoints(points: DeskViewPoint[]): DeskViewPoint[] {
  const source = Array.isArray(points) && points.length === 4 ? points : DEFAULT_POINTS;
  return source.map((point, index) => ({
    x: clamp01(Number(point?.x ?? DEFAULT_POINTS[index].x)),
    y: clamp01(Number(point?.y ?? DEFAULT_POINTS[index].y)),
    label: point?.label ?? DEFAULT_POINTS[index].label,
  }));
}

function fitRect(containerWidth: number, containerHeight: number, contentWidth: number, contentHeight: number): Rect {
  const scale = Math.min(containerWidth / contentWidth, containerHeight / contentHeight);
  const width = contentWidth * scale;
  const height = contentHeight * scale;
  return {
    x: (containerWidth - width) / 2,
    y: (containerHeight - height) / 2,
    width,
    height,
  };
}

function getCorrectedCanvas(
  sourceCanvas: HTMLCanvasElement,
  correctedCanvasRef: React.MutableRefObject<HTMLCanvasElement | null>,
  correctedRendererRef: React.MutableRefObject<WebGlEffectRenderer | null>,
  fx: number,
  fy: number,
  scale: number,
  meshSteps: number,
): HTMLCanvasElement {
  if (!fx && !fy && scale === 1) {
    return sourceCanvas;
  }
  let correctedCanvas = correctedCanvasRef.current;
  if (!correctedCanvas) {
    correctedCanvas = document.createElement('canvas');
    correctedCanvasRef.current = correctedCanvas;
  }
  if (!correctedRendererRef.current) {
    try {
      correctedRendererRef.current = new WebGlEffectRenderer(correctedCanvas);
    } catch {
      correctedRendererRef.current = null;
    }
  }
  const width = Math.max(1, sourceCanvas.width);
  const height = Math.max(1, sourceCanvas.height);
  if (correctedRendererRef.current?.draw(sourceCanvas, { fx, fy, scale })) {
    return correctedCanvas;
  }
  if (correctedCanvas.width !== width || correctedCanvas.height !== height) {
    correctedCanvas.width = width;
    correctedCanvas.height = height;
  }
  const ctx = correctedCanvas.getContext('2d');
  if (!ctx) {
    return sourceCanvas;
  }
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.clearRect(0, 0, width, height);
  drawWarpMesh(
    ctx,
    sourceCanvas,
    (u, v) => {
      const mapped = displayToSourceTexture({ x: u, y: v }, fx, fy, scale);
      return {
        src: {
          x: mapped.x * width,
          y: mapped.y * height,
        },
        dst: {
          x: u * width,
          y: v * height,
        },
      };
    },
    meshSteps,
    meshSteps,
  );
  return correctedCanvas;
}
