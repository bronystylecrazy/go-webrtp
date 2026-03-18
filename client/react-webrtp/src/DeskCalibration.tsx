import { useEffect, useMemo, useRef, useState } from 'react';
import { Circle, Group, Layer, Line, Stage, Text } from 'react-konva';
import type {
  WebRtpClientOptions,
  WebRtpErrorCallback,
  WebRtpEventCallback,
  WebRtpFrameCallback,
  WebRtpInfo,
  WebRtpInfoCallback,
} from './client';
import { DeskView, type DeskViewHandle, type DeskViewPoint } from './DeskView';
import { clamp01, sourceToDisplayTexture } from './deskTransform';
import { VideoPlayer, type VideoPlayerProps } from './VideoPlayer';
import { useWebRtpStream } from './WebRtpProvider';

export interface DeskCalibrationState {
  points: DeskViewPoint[];
  guides: DeskViewPoint[];
  allPoints: DeskCalibrationPoint[];
  fx: number;
  fy: number;
  scale: number;
}

export type DeskCalibrationPointKind = 'A' | 'AB' | 'B' | 'BC' | 'C' | 'CD' | 'D' | 'DA';

export interface DeskCalibrationPoint extends Omit<DeskViewPoint, 'label'> {
  kind: DeskCalibrationPointKind;
  label?: string;
}

export interface DeskCalibrationHandleRenderProps {
  point: DeskCalibrationPoint;
  kind: 'corner' | 'guide';
  index: number;
}

export interface DeskCalibrationProps
  extends Omit<React.HTMLAttributes<HTMLDivElement>, 'onChange'>,
    WebRtpClientOptions {
  url: string;
  showVideo?: boolean;
  initialPoints?: DeskViewPoint[];
  initialAllPoints?: DeskCalibrationPoint[];
  allPoints?: DeskCalibrationPoint[];
  initialFx?: number;
  initialFy?: number;
  initialScale?: number;
  minScale?: number;
  maxScale?: number;
  overlayOnly?: boolean;
  showControls?: boolean;
  showCode?: boolean;
  lineColor?: string;
  guideLineColor?: string;
  onAllPointsChange?: (points: DeskCalibrationPoint[]) => void;
  onChange?: (state: DeskCalibrationState) => void;
  onEvent?: (text: string) => void;
  renderHandle?: (props: DeskCalibrationHandleRenderProps) => React.ReactNode;
  videoProps?: Omit<React.VideoHTMLAttributes<HTMLVideoElement>, 'src' | 'srcObject' | 'onError'>;
  onInfo?: WebRtpInfoCallback;
  onFrame?: WebRtpFrameCallback;
  onPlayerError?: WebRtpErrorCallback;
}

const DEFAULT_POINTS: DeskViewPoint[] = [
  { x: 0.2, y: 0.2, label: 'A' },
  { x: 0.8, y: 0.24, label: 'B' },
  { x: 0.88, y: 0.82, label: 'C' },
  { x: 0.12, y: 0.78, label: 'D' },
];

export function DeskCalibration({
  url,
  showVideo = true,
  initialPoints = DEFAULT_POINTS,
  initialAllPoints,
  allPoints: controlledAllPoints,
  initialFx = 0.08,
  initialFy = 0.15,
  initialScale = 1,
  minScale = 0.85,
  maxScale = 1.15,
  overlayOnly = false,
  showControls = true,
  showCode = false,
  lineColor = 'rgba(248, 250, 252, 0.72)',
  guideLineColor = 'rgba(248, 250, 252, 0.28)',
  onAllPointsChange,
  onChange,
  onEvent,
  renderHandle,
  videoProps,
  onInfo,
  onFrame,
  onPlayerError,
  autoReconnect,
  reconnectDelayMs,
  maxReconnectDelayMs,
  lateFrameThreshold,
  maxPendingDecode,
  style,
  ...divProps
}: DeskCalibrationProps) {
  const stream = useWebRtpStream(url, {
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
  });
  const client = stream.client;
  const [info, setInfo] = useState<WebRtpInfo>({});
  const initialCornerPoints = normalizeDeskPoints(initialPoints);
  const [internalAllPoints, setInternalAllPoints] = useState<DeskCalibrationPoint[]>(
    normalizeAllPoints(initialAllPoints ?? createAllPoints(initialCornerPoints, defaultDeskGuides(initialCornerPoints))),
  );
  const [fx, setFx] = useState(initialFx);
  const [fy, setFy] = useState(initialFy);
  const [scale, setScale] = useState(initialScale);
  const deskViewRef = useRef<DeskViewHandle | null>(null);
  const resolvedAllPoints = useMemo(
    () => normalizeAllPoints(controlledAllPoints ?? internalAllPoints),
    [controlledAllPoints, internalAllPoints],
  );
  const points = useMemo(() => getCornerPoints(resolvedAllPoints), [resolvedAllPoints]);
  const guides = useMemo(() => getGuidePoints(resolvedAllPoints), [resolvedAllPoints]);

  useEffect(() => {
    if (!client) {
      return;
    }
    const offInfo = client.onInfo((nextInfo) => {
      setInfo((prev) => ({ ...prev, ...nextInfo }));
      onInfo?.(nextInfo);
    });
    const offFrame = client.onFrame((frameNo, data, isKey) => {
      onFrame?.(frameNo, data, isKey);
    });
    const offError = client.onError((nextError) => {
      onPlayerError?.(nextError);
    });
    return () => {
      offInfo();
      offFrame();
      offError();
    };
  }, [client, onInfo, onFrame, onPlayerError]);

  useEffect(() => {
    deskViewRef.current?.redraw();
    onChange?.({ points, guides, allPoints: resolvedAllPoints, fx, fy, scale });
  }, [points, guides, resolvedAllPoints, fx, fy, scale, onChange]);

  const applyDistortionSettings = (nextFx: number, nextFy: number, nextScale: number) => {
    setFx(nextFx);
    setFy(nextFy);
    setScale(nextScale);
  };

  const updateAllPoints = (nextAllPoints: DeskCalibrationPoint[]) => {
    const normalized = normalizeAllPoints(nextAllPoints);
    if (!controlledAllPoints) {
      setInternalAllPoints(normalized);
    }
    onAllPointsChange?.(normalized);
  };

  const updateCornersAndGuides = (nextPoints: DeskViewPoint[], nextGuides: DeskViewPoint[]) => {
    updateAllPoints(createAllPoints(nextPoints, nextGuides));
  };

  return (
    overlayOnly ? (
      <DeskSourcePanel
        {...divProps}
        showVideo={false}
        showHiddenOverlay={false}
        shellStyle={{
          position: 'absolute',
          inset: 0,
          width: '100%',
          height: '100%',
          aspectRatio: 'auto',
          border: 'none',
          borderRadius: 0,
          background: 'transparent',
          ...style,
        }}
        url={url}
        info={info}
        points={points}
        guides={guides}
        currentFx={fx}
        currentFy={fy}
        currentScale={scale}
        minScale={minScale}
        maxScale={maxScale}
        lineColor={lineColor}
        guideLineColor={guideLineColor}
        videoProps={videoProps}
        onAllPointsChange={updateAllPoints}
        onPointsChange={(nextPoints) => updateCornersAndGuides(nextPoints, guides)}
        onGuidesChange={(nextGuides) => updateCornersAndGuides(points, nextGuides)}
        onDistortionChange={(next) => {
          setFx(next.fx);
          setFy(next.fy);
          setScale(next.scale);
        }}
        onEvent={onEvent}
        renderHandle={renderHandle}
      />
    ) : (
    <div
      {...divProps}
      style={{
        display: 'grid',
        gap: 18,
        ...style,
      }}
    >
      {showControls ? (
        <section style={styles.effectPanel}>
          <div style={styles.effectHeader}>
            <strong>DeskView distortion</strong>
            <div style={styles.controlsRow}>
              <button
                type="button"
                onClick={() => {
                  updateAllPoints(normalizeAllPoints(initialAllPoints ?? createAllPoints(initialCornerPoints, defaultDeskGuides(initialCornerPoints))));
                }}
                style={styles.secondaryButton}
              >
                Reset points
              </button>
              <button
                type="button"
                onClick={() => {
                  const detection = detectDeskFromFrame(client?.getCanvas() ?? null, minScale, maxScale);
                  const nextPoints = detection?.points ?? points;
                  const nextGuides = detection?.guides ?? guides;
                  const optimized = optimizeDeskDistortion(nextPoints, nextGuides, fx, fy, scale, minScale, maxScale);
                  updateCornersAndGuides(nextPoints, nextGuides);
                  setFx(optimized.fx);
                  setFy(optimized.fy);
                  setScale(1);
                }}
                style={styles.secondaryButton}
              >
                Auto detect
              </button>
              <button
                type="button"
                onClick={() => {
                  applyDistortionSettings(0, 0, 1);
                }}
                style={styles.secondaryButton}
              >
                Reset effects
              </button>
            </div>
          </div>
          <div style={styles.effectGrid}>
            <SliderControl label="fx" min={-0.25} max={0.25} step={0.01} value={fx} onChange={(value) => applyDistortionSettings(value, fy, scale)} />
            <SliderControl label="fy" min={-0.25} max={0.25} step={0.01} value={fy} onChange={(value) => applyDistortionSettings(fx, value, scale)} />
            <SliderControl label="scale" min={minScale} max={maxScale} step={0.01} value={scale} onChange={(value) => applyDistortionSettings(fx, fy, value)} />
          </div>
        </section>
      ) : null}

      <section style={styles.viewerPanel}>
        <div style={styles.viewerHeader}>
          <strong>Raw stream</strong>
          <span style={styles.viewerMeta}>Drag the Konva points</span>
        </div>
        <DeskSourcePanel
          showVideo={showVideo}
          url={url}
          info={info}
          points={points}
          guides={guides}
          currentFx={fx}
          currentFy={fy}
          currentScale={scale}
          minScale={minScale}
          maxScale={maxScale}
          lineColor={lineColor}
          guideLineColor={guideLineColor}
          videoProps={videoProps}
          onAllPointsChange={updateAllPoints}
          onPointsChange={(nextPoints) => updateCornersAndGuides(nextPoints, guides)}
          onGuidesChange={(nextGuides) => updateCornersAndGuides(points, nextGuides)}
          onDistortionChange={(next) => {
            setFx(next.fx);
            setFy(next.fy);
            setScale(next.scale);
          }}
          onEvent={onEvent}
          renderHandle={renderHandle}
        />
      </section>

      <section style={styles.viewerPanel}>
        <div style={styles.viewerHeader}>
          <strong>DeskView</strong>
          <span style={styles.viewerMeta}>{`fx=${fx.toFixed(2)} fy=${fy.toFixed(2)} scale=${scale.toFixed(2)}`}</span>
        </div>
        <div style={styles.deskViewShell}>
          <DeskView
            ref={deskViewRef}
            url={url}
            points={points}
            fx={fx}
            dy={fy}
            scale={scale}
            style={styles.deskView}
          />
        </div>
        {showCode ? <pre style={styles.codeBlock}>{formatDeskPoints(points)}</pre> : null}
      </section>
    </div>
    )
  );
}

interface SliderControlProps {
  label: string;
  min: number;
  max: number;
  step: number;
  value: number;
  onChange: (value: number) => void;
}

function SliderControl({ label, min, max, step, value, onChange }: SliderControlProps) {
  return (
    <label style={styles.sliderControl}>
      <span style={styles.sliderLabelRow}>
        <span style={styles.sliderLabel}>{label}</span>
        <span style={styles.sliderValue}>{value.toFixed(2)}</span>
      </span>
      <input type="range" min={min} max={max} step={step} value={value} onChange={(event) => onChange(Number(event.target.value))} style={styles.slider} />
    </label>
  );
}

interface DeskSourcePanelProps {
  className?: string;
  style?: React.CSSProperties;
  shellStyle?: React.CSSProperties;
  showVideo: boolean;
  showHiddenOverlay?: boolean;
  url: string;
  info: WebRtpInfo;
  points: DeskViewPoint[];
  guides: DeskViewPoint[];
  currentFx: number;
  currentFy: number;
  currentScale: number;
  minScale: number;
  maxScale: number;
  lineColor: string;
  guideLineColor: string;
  onPointsChange: (points: DeskViewPoint[]) => void;
  onGuidesChange: (points: DeskViewPoint[]) => void;
  onAllPointsChange?: (points: DeskCalibrationPoint[]) => void;
  onDistortionChange: (value: { fx: number; fy: number; scale: number }) => void;
  onEvent?: (text: string) => void;
  renderHandle?: (props: DeskCalibrationHandleRenderProps) => React.ReactNode;
  videoProps?: Omit<React.VideoHTMLAttributes<HTMLVideoElement>, 'src' | 'srcObject' | 'onError'>;
}

function DeskSourcePanel({
  className,
  style,
  shellStyle,
  showVideo,
  showHiddenOverlay = true,
  url,
  info,
  points,
  guides,
  currentFx,
  currentFy,
  currentScale,
  minScale,
  maxScale,
  lineColor,
  guideLineColor,
  onPointsChange,
  onGuidesChange,
  onAllPointsChange,
  onDistortionChange,
  onEvent,
  renderHandle,
  videoProps,
}: DeskSourcePanelProps) {
  const shellRef = useRef<HTMLDivElement | null>(null);
  const [shellSize, setShellSize] = useState({ width: 0, height: 0 });

  useEffect(() => {
    const node = shellRef.current;
    if (!node) {
      return;
    }
    const observer = new ResizeObserver(() => {
      setShellSize({ width: node.clientWidth, height: node.clientHeight });
    });
    observer.observe(node);
    setShellSize({ width: node.clientWidth, height: node.clientHeight });
    return () => observer.disconnect();
  }, []);

  const layout = getVideoLayout(shellSize.width, shellSize.height, Number(info.width || 0), Number(info.height || 0));
  const stagePoints = points.map((point) => ({
    x: layout.offsetX + point.x * layout.displayWidth,
    y: layout.offsetY + point.y * layout.displayHeight,
    label: point.label ?? '',
  }));
  const stageGuides = guides.map((point) => ({
    x: layout.offsetX + point.x * layout.displayWidth,
    y: layout.offsetY + point.y * layout.displayHeight,
    label: point.label ?? '',
  }));

  return (
    <div ref={shellRef} className={className} style={{ ...styles.playerShell, ...style, ...shellStyle }}>
      {showVideo ? (
        <VideoPlayer
          key={`player:${url}`}
          url={url}
          autoPlay
          muted
          playsInline
          onEvent={(event) => {
            onEvent?.(
              [event.type, event.decoder ? `decoder=${event.decoder}` : '', event.codec ? `codec=${event.codec}` : '']
                .filter(Boolean)
                .join(' '),
            );
          }}
          style={styles.video}
          {...videoProps}
        />
      ) : showHiddenOverlay ? (
        <div style={styles.hiddenOverlay}>
          <span style={styles.hiddenText}>Video player hidden.</span>
        </div>
      ) : null}

      {layout.displayWidth > 0 && layout.displayHeight > 0 ? (
        <Stage width={shellSize.width} height={shellSize.height} style={styles.stage}>
          <Layer listening={false}>
            {stageGuides.length >= 4 ? (
              <Line
                points={[
                  stagePoints[0].x, stagePoints[0].y,
                  stageGuides[0].x, stageGuides[0].y,
                  stagePoints[1].x, stagePoints[1].y,
                  stageGuides[1].x, stageGuides[1].y,
                  stagePoints[2].x, stagePoints[2].y,
                  stageGuides[2].x, stageGuides[2].y,
                  stagePoints[3].x, stagePoints[3].y,
                  stageGuides[3].x, stageGuides[3].y,
                  stagePoints[0].x, stagePoints[0].y,
                ]}
                stroke={lineColor}
                strokeWidth={2}
              />
            ) : null}
            {stageGuides.map((point) => (
              <Line
                key={`guide-line-${point.label}`}
                points={
                  point.label === 'AB'
                    ? [point.x, point.y, point.x, layout.offsetY]
                    : point.label === 'CD'
                      ? [point.x, point.y, point.x, layout.offsetY + layout.displayHeight]
                      : point.label === 'BC'
                        ? [point.x, point.y, layout.offsetX + layout.displayWidth, point.y]
                        : [point.x, point.y, layout.offsetX, point.y]
                }
                stroke={guideLineColor}
                dash={[6, 6]}
                strokeWidth={1}
              />
            ))}
          </Layer>
          <Layer>
            {stagePoints.map((point, index) => (
              <Group
                key={point.label || index}
                x={point.x}
                y={point.y}
                draggable
                onDragMove={(event) => {
                  const nextX = clamp(event.target.x(), layout.offsetX, layout.offsetX + layout.displayWidth);
                  const nextY = clamp(event.target.y(), layout.offsetY, layout.offsetY + layout.displayHeight);
                  const sourcePoint = {
                    x: clamp01((nextX - layout.offsetX) / layout.displayWidth),
                    y: clamp01((nextY - layout.offsetY) / layout.displayHeight),
                  };
                  const previousPoints = points.map((item) => ({ ...item }));
                  const previousGuides = guides.map((item) => ({ ...item }));
                  const nextPoints = points.map((item, itemIndex) =>
                    itemIndex === index ? { ...item, x: sourcePoint.x, y: sourcePoint.y } : item,
                  );
                  event.target.position({ x: nextX, y: nextY });
                  const nextGuides = [
                    remapGuidePointForEdge(previousGuides[0] || midpoint(previousPoints[0], previousPoints[1], 'AB'), previousPoints[0], previousPoints[1], nextPoints[0], nextPoints[1], 'AB'),
                    remapGuidePointForEdge(previousGuides[1] || midpoint(previousPoints[1], previousPoints[2], 'BC'), previousPoints[1], previousPoints[2], nextPoints[1], nextPoints[2], 'BC'),
                    remapGuidePointForEdge(previousGuides[2] || midpoint(previousPoints[3], previousPoints[2], 'CD'), previousPoints[3], previousPoints[2], nextPoints[3], nextPoints[2], 'CD'),
                    remapGuidePointForEdge(previousGuides[3] || midpoint(previousPoints[0], previousPoints[3], 'DA'), previousPoints[0], previousPoints[3], nextPoints[0], nextPoints[3], 'DA'),
                  ];
                  if (onAllPointsChange) {
                    onAllPointsChange(createAllPoints(nextPoints, nextGuides));
                  } else {
                    onPointsChange(nextPoints);
                    onGuidesChange(nextGuides);
                  }
                }}
              >
                {renderHandle?.({ point: toCalibrationPoint(points[index], (['A', 'B', 'C', 'D'] as const)[index]), kind: 'corner', index }) ?? (
                  <>
                    <Circle radius={8} fill="#0f172a" stroke="#7dd3fc" strokeWidth={1.5} shadowColor="rgba(34, 211, 238, 0.35)" shadowBlur={10} />
                    <Circle radius={3} fill="#38bdf8" />
                    <Text text={points[index].label ?? (['A', 'B', 'C', 'D'] as const)[index]} x={-6} y={-22} width={12} align="center" fill="#e0f2fe" fontSize={11} fontStyle="bold" />
                  </>
                )}
              </Group>
            ))}
            {stageGuides.map((point, index) => (
              <Group
                key={point.label || `guide-${index}`}
                x={point.x}
                y={point.y}
                draggable
                onDragMove={(event) => {
                  const nextX = clamp(event.target.x(), layout.offsetX, layout.offsetX + layout.displayWidth);
                  const nextY = clamp(event.target.y(), layout.offsetY, layout.offsetY + layout.displayHeight);
                  const sourcePoint = {
                    x: clamp01((nextX - layout.offsetX) / layout.displayWidth),
                    y: clamp01((nextY - layout.offsetY) / layout.displayHeight),
                  };
                  event.target.position({ x: nextX, y: nextY });
                  const nextGuides = guides.map((item, itemIndex) =>
                    itemIndex === index ? { ...item, x: sourcePoint.x, y: sourcePoint.y } : item,
                  );
                  if (onAllPointsChange) {
                    onAllPointsChange(createAllPoints(points, nextGuides));
                  } else {
                    onGuidesChange(nextGuides);
                  }
                  onDistortionChange(optimizeDeskDistortion(points, nextGuides, currentFx, currentFy, currentScale, minScale, maxScale));
                }}
              >
                {renderHandle?.({ point: toCalibrationPoint(guides[index], (['AB', 'BC', 'CD', 'DA'] as const)[index]), kind: 'guide', index }) ?? (
                  <>
                    <Circle radius={6} fill="#082f49" stroke="#f8fafc" strokeWidth={1.5} />
                    <Circle radius={2.5} fill="#f8fafc" />
                    <Text text={guides[index].label ?? (['AB', 'BC', 'CD', 'DA'] as const)[index]} x={-10} y={10} width={20} align="center" fill="#f8fafc" fontSize={10} fontStyle="bold" />
                  </>
                )}
              </Group>
            ))}
          </Layer>
        </Stage>
      ) : null}
    </div>
  );
}

function getVideoLayout(containerWidth: number, containerHeight: number, sourceWidth: number, sourceHeight: number) {
  if (containerWidth <= 0 || containerHeight <= 0 || sourceWidth <= 0 || sourceHeight <= 0) {
    return { displayWidth: 0, displayHeight: 0, offsetX: 0, offsetY: 0 };
  }
  const scale = Math.min(containerWidth / sourceWidth, containerHeight / sourceHeight);
  const displayWidth = sourceWidth * scale;
  const displayHeight = sourceHeight * scale;
  return { displayWidth, displayHeight, offsetX: (containerWidth - displayWidth) / 2, offsetY: (containerHeight - displayHeight) / 2 };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function midpoint(a: DeskViewPoint, b: DeskViewPoint, label: string): DeskViewPoint {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, label };
}

function defaultDeskGuides(points: DeskViewPoint[]): DeskViewPoint[] {
  return [
    midpoint(points[0], points[1], 'AB'),
    midpoint(points[1], points[2], 'BC'),
    midpoint(points[3], points[2], 'CD'),
    midpoint(points[0], points[3], 'DA'),
  ];
}

function createAllPoints(points: DeskViewPoint[], guides: DeskViewPoint[]): DeskCalibrationPoint[] {
  const normalizedPoints = normalizeDeskPoints(points);
  const normalizedGuides = normalizeGuidePoints(guides, normalizedPoints);
  return [
    toCalibrationPoint(normalizedPoints[0], 'A'),
    toCalibrationPoint(normalizedGuides[0], 'AB'),
    toCalibrationPoint(normalizedPoints[1], 'B'),
    toCalibrationPoint(normalizedGuides[1], 'BC'),
    toCalibrationPoint(normalizedPoints[2], 'C'),
    toCalibrationPoint(normalizedGuides[2], 'CD'),
    toCalibrationPoint(normalizedPoints[3], 'D'),
    toCalibrationPoint(normalizedGuides[3], 'DA'),
  ];
}

function normalizeGuidePoints(guides: DeskViewPoint[], points: DeskViewPoint[]): DeskViewPoint[] {
  const fallback = defaultDeskGuides(points);
  return fallback.map((point, index) => ({
    x: clamp01(Number(guides[index]?.x ?? point.x)),
    y: clamp01(Number(guides[index]?.y ?? point.y)),
    label: point.label,
  }));
}

function normalizeAllPoints(points: DeskCalibrationPoint[]): DeskCalibrationPoint[] {
  const fallbackCorners = normalizeDeskPoints(DEFAULT_POINTS);
  const fallback = createAllPoints(fallbackCorners, defaultDeskGuides(fallbackCorners));
  const pointMap = new Map(
    (Array.isArray(points) ? points : []).map((point) => [resolvePointKind(point), point]),
  );
  return (['A', 'AB', 'B', 'BC', 'C', 'CD', 'D', 'DA'] as DeskCalibrationPointKind[]).map((kind, index) => ({
    x: clamp01(Number(pointMap.get(kind)?.x ?? fallback[index].x)),
    y: clamp01(Number(pointMap.get(kind)?.y ?? fallback[index].y)),
    kind,
    label: pointMap.get(kind)?.label ?? fallback[index].label ?? kind,
  }));
}

function getCornerPoints(allPoints: DeskCalibrationPoint[]): DeskViewPoint[] {
  return [allPoints[0], allPoints[2], allPoints[4], allPoints[6]].map((point) => ({ x: point.x, y: point.y, label: point.kind }));
}

function getGuidePoints(allPoints: DeskCalibrationPoint[]): DeskViewPoint[] {
  return [allPoints[1], allPoints[3], allPoints[5], allPoints[7]].map((point) => ({ x: point.x, y: point.y, label: point.kind }));
}

function toCalibrationPoint(point: DeskViewPoint, kind: DeskCalibrationPointKind): DeskCalibrationPoint {
  return {
    x: clamp01(Number(point.x)),
    y: clamp01(Number(point.y)),
    kind,
    label: point.label ?? kind,
  };
}

function resolvePointKind(point: DeskCalibrationPoint | undefined): DeskCalibrationPointKind | undefined {
  const kind = point?.kind ?? point?.label;
  if (kind === 'A' || kind === 'AB' || kind === 'B' || kind === 'BC' || kind === 'C' || kind === 'CD' || kind === 'D' || kind === 'DA') {
    return kind;
  }
  return undefined;
}

function normalizeDeskPoints(points: DeskViewPoint[]): DeskViewPoint[] {
  const source = Array.isArray(points) && points.length === 4 ? points : DEFAULT_POINTS;
  return source.map((point, index) => ({
    x: clamp01(Number(point?.x ?? DEFAULT_POINTS[index].x)),
    y: clamp01(Number(point?.y ?? DEFAULT_POINTS[index].y)),
    label: point?.label ?? DEFAULT_POINTS[index].label,
  }));
}

function remapGuidePointForEdge(
  guidePoint: DeskViewPoint,
  oldStart: DeskViewPoint,
  oldEnd: DeskViewPoint,
  newStart: DeskViewPoint,
  newEnd: DeskViewPoint,
  label: string,
): DeskViewPoint {
  const oldDx = oldEnd.x - oldStart.x;
  const oldDy = oldEnd.y - oldStart.y;
  const oldLenSq = oldDx * oldDx + oldDy * oldDy;
  const newDx = newEnd.x - newStart.x;
  const newDy = newEnd.y - newStart.y;
  const newLen = Math.hypot(newDx, newDy);
  if (oldLenSq <= 0.000001 || newLen <= 0.000001) {
    return midpoint(newStart, newEnd, label);
  }
  const relX = guidePoint.x - oldStart.x;
  const relY = guidePoint.y - oldStart.y;
  const t = ((relX * oldDx) + (relY * oldDy)) / oldLenSq;
  const oldLen = Math.sqrt(oldLenSq);
  const nx = -oldDy / oldLen;
  const ny = oldDx / oldLen;
  const offset = relX * nx + relY * ny;
  const newNx = -newDy / newLen;
  const newNy = newDx / newLen;
  return { x: clamp01(newStart.x + newDx * t + newNx * offset), y: clamp01(newStart.y + newDy * t + newNy * offset), label };
}

function pointLineDistance(point: DeskViewPoint, start: DeskViewPoint, end: DeskViewPoint): number {
  const dx = end.x - start.x;
  const dy = end.y - start.y;
  const length = Math.hypot(dx, dy);
  if (length <= 0.000001) {
    return Math.hypot(point.x - start.x, point.y - start.y);
  }
  return Math.abs(dy * point.x - dx * point.y + end.x * start.y - end.y * start.x) / length;
}

function pointSegmentDistance(point: DeskViewPoint, start: DeskViewPoint, end: DeskViewPoint): number {
  const dx = end.x - start.x;
  const dy = end.y - start.y;
  const denom = dx * dx + dy * dy;
  if (denom <= 0.000001) {
    return Math.hypot(point.x - start.x, point.y - start.y);
  }
  const projection = ((point.x - start.x) * dx + (point.y - start.y) * dy) / denom;
  const t = clamp(projection, 0, 1);
  const closestX = start.x + dx * t;
  const closestY = start.y + dy * t;
  return Math.hypot(point.x - closestX, point.y - closestY);
}

function sampleFrameLuma(canvas: HTMLCanvasElement | null, maxWidth = 320) {
  if (!canvas || canvas.width <= 0 || canvas.height <= 0) {
    return null;
  }
  const scale = Math.min(1, maxWidth / canvas.width);
  const width = Math.max(32, Math.round(canvas.width * scale));
  const height = Math.max(24, Math.round(canvas.height * scale));
  const sampleCanvas = document.createElement('canvas');
  sampleCanvas.width = width;
  sampleCanvas.height = height;
  const sampleCtx = sampleCanvas.getContext('2d', { willReadFrequently: true });
  if (!sampleCtx) {
    return null;
  }
  sampleCtx.drawImage(canvas, 0, 0, width, height);
  const data = sampleCtx.getImageData(0, 0, width, height).data;
  const luma = new Float32Array(width * height);
  for (let i = 0, p = 0; i < luma.length; i++, p += 4) {
    luma[i] = data[p] * 0.299 + data[p + 1] * 0.587 + data[p + 2] * 0.114;
  }
  return { width, height, luma };
}

function lumaAt(sample: { width: number; height: number; luma: Float32Array }, x: number, y: number): number {
  const xx = Math.min(sample.width - 1, Math.max(0, x));
  const yy = Math.min(sample.height - 1, Math.max(0, y));
  return sample.luma[yy * sample.width + xx];
}

function detectDeskFromFrame(canvas: HTMLCanvasElement | null, minScale: number, maxScale: number) {
  const sample = sampleFrameLuma(canvas, 320);
  if (!sample) {
    return null;
  }
  const topStart = Math.floor(sample.height * 0.12);
  const topEnd = Math.floor(sample.height * 0.58);
  let bestTopY = topStart;
  let bestTopScore = -Infinity;
  for (let y = topStart; y < topEnd; y++) {
    let score = 0;
    let count = 0;
    for (let x = Math.floor(sample.width * 0.12); x < Math.floor(sample.width * 0.88); x += 2) {
      const above = lumaAt(sample, x, y - 2);
      const below = lumaAt(sample, x, y + 2);
      score += Math.max(0, below - above) + Math.abs(below - above) * 0.35;
      count++;
    }
    score /= Math.max(1, count);
    if (score > bestTopScore) {
      bestTopScore = score;
      bestTopY = y;
    }
  }
  const bottomStart = Math.max(bestTopY + 10, Math.floor(sample.height * 0.52));
  const bottomEnd = Math.floor(sample.height * 0.96);
  let bestBottomY = bottomStart;
  let bestBottomScore = -Infinity;
  for (let y = bottomStart; y < bottomEnd; y++) {
    let score = 0;
    let count = 0;
    for (let x = Math.floor(sample.width * 0.1); x < Math.floor(sample.width * 0.9); x += 2) {
      const above = lumaAt(sample, x, y - 2);
      const below = lumaAt(sample, x, y + 2);
      score += Math.max(0, above - below) + Math.abs(below - above) * 0.25;
      count++;
    }
    score /= Math.max(1, count);
    if (score > bestBottomScore) {
      bestBottomScore = score;
      bestBottomY = y;
    }
  }
  const detectVerticalEdge = (fromLeft: boolean) => {
    const startX = fromLeft ? Math.floor(sample.width * 0.02) : Math.floor(sample.width * 0.65);
    const endX = fromLeft ? Math.floor(sample.width * 0.35) : Math.floor(sample.width * 0.98);
    let bestX = startX;
    let bestScore = -Infinity;
    for (let x = startX; x < endX; x++) {
      let score = 0;
      let count = 0;
      for (let y = bestTopY + 4; y < bestBottomY - 4; y += 2) {
        const left = lumaAt(sample, x - 2, y);
        const right = lumaAt(sample, x + 2, y);
        const delta = fromLeft ? right - left : left - right;
        score += Math.max(0, delta) + Math.abs(delta) * 0.2;
        count++;
      }
      score /= Math.max(1, count);
      if (score > bestScore) {
        bestScore = score;
        bestX = x;
      }
    }
    return bestX;
  };
  const leftX = detectVerticalEdge(true);
  const rightX = detectVerticalEdge(false);
  if (!(rightX > leftX + sample.width * 0.2 && bestBottomY > bestTopY + sample.height * 0.18)) {
    return null;
  }
  const refineRowPeak = (x0: number, x1: number, yCenter: number, mode: 'top' | 'bottom') => {
    let bestY = yCenter;
    let bestScore = -Infinity;
    const yMin = Math.max(2, yCenter - Math.floor(sample.height * 0.08));
    const yMax = Math.min(sample.height - 3, yCenter + Math.floor(sample.height * 0.08));
    for (let y = yMin; y <= yMax; y++) {
      let score = 0;
      let count = 0;
      for (let x = x0; x <= x1; x += 2) {
        const above = lumaAt(sample, x, y - 2);
        const below = lumaAt(sample, x, y + 2);
        const delta = mode === 'top' ? below - above : above - below;
        score += Math.max(0, delta);
        count++;
      }
      score /= Math.max(1, count);
      if (score > bestScore) {
        bestScore = score;
        bestY = y;
      }
    }
    return bestY;
  };
  const inset = Math.max(2, Math.round((rightX - leftX) * 0.05));
  const tlY = refineRowPeak(leftX, Math.min(sample.width - 1, leftX + inset * 3), bestTopY, 'top');
  const trY = refineRowPeak(Math.max(0, rightX - inset * 3), rightX, bestTopY, 'top');
  const blY = refineRowPeak(leftX, Math.min(sample.width - 1, leftX + inset * 3), bestBottomY, 'bottom');
  const brY = refineRowPeak(Math.max(0, rightX - inset * 3), rightX, bestBottomY, 'bottom');
  const centerX = Math.round((leftX + rightX) / 2);
  const centerInset = Math.max(4, Math.round((rightX - leftX) * 0.14));
  const topMidY = refineRowPeak(Math.max(leftX, centerX - centerInset), Math.min(rightX, centerX + centerInset), bestTopY, 'top');
  const bottomMidY = refineRowPeak(Math.max(leftX, centerX - centerInset), Math.min(rightX, centerX + centerInset), bestBottomY, 'bottom');
  const points: DeskViewPoint[] = [
    { x: leftX / sample.width, y: tlY / sample.height, label: 'A' },
    { x: rightX / sample.width, y: trY / sample.height, label: 'B' },
    { x: rightX / sample.width, y: brY / sample.height, label: 'C' },
    { x: leftX / sample.width, y: blY / sample.height, label: 'D' },
  ];
  const topWidth = Math.hypot(points[1].x - points[0].x, points[1].y - points[0].y);
  const bottomWidth = Math.hypot(points[2].x - points[3].x, points[2].y - points[3].y);
  const leftHeight = Math.hypot(points[3].x - points[0].x, points[3].y - points[0].y);
  const rightHeight = Math.hypot(points[2].x - points[1].x, points[2].y - points[1].y);
  const widthBias = (bottomWidth - topWidth) / Math.max(0.001, bottomWidth + topWidth);
  const heightBias = (rightHeight - leftHeight) / Math.max(0.001, rightHeight + leftHeight);
  const detectedScaleBias = Math.max(Math.abs(widthBias), Math.abs(heightBias)) * 0.35;
  return {
    points,
    guides: [
      { x: centerX / sample.width, y: topMidY / sample.height, label: 'AB' },
      midpoint(points[1], points[2], 'BC'),
      { x: centerX / sample.width, y: bottomMidY / sample.height, label: 'CD' },
      midpoint(points[0], points[3], 'DA'),
    ],
    scale: clamp(1 / (1 + detectedScaleBias), minScale, maxScale),
  };
}

function optimizeDeskDistortion(
  points: DeskViewPoint[],
  guides: DeskViewPoint[],
  currentFx: number,
  currentFy: number,
  currentScale: number,
  minScale: number,
  maxScale: number,
) {
  let bestFx = clamp(currentFx, -0.25, 0.25);
  let bestFy = clamp(currentFy, -0.25, 0.25);
  let bestScale = 1;
  let bestScore = Number.POSITIVE_INFINITY;
  const scoreCandidate = (fx: number, fy: number, scale: number) => {
    const mappedCorners = points.map((point) => sourceToDisplayTexture(point, fx, fy, scale));
    const mappedGuides = Object.fromEntries(guides.map((point) => [point.label ?? '', sourceToDisplayTexture(point, fx, fy, scale)]));
    const topGuide = mappedGuides.AB ?? midpoint(mappedCorners[0], mappedCorners[1], 'AB');
    const rightGuide = mappedGuides.BC ?? midpoint(mappedCorners[1], mappedCorners[2], 'BC');
    const bottomGuide = mappedGuides.CD ?? midpoint(mappedCorners[3], mappedCorners[2], 'CD');
    const leftGuide = mappedGuides.DA ?? midpoint(mappedCorners[0], mappedCorners[3], 'DA');
    const topLineError = pointLineDistance(topGuide, mappedCorners[0], mappedCorners[1]);
    const bottomLineError = pointLineDistance(bottomGuide, mappedCorners[3], mappedCorners[2]);
    const rightLineError = pointLineDistance(rightGuide, mappedCorners[1], mappedCorners[2]);
    const leftLineError = pointLineDistance(leftGuide, mappedCorners[0], mappedCorners[3]);
    const topSegmentError = pointSegmentDistance(topGuide, mappedCorners[0], mappedCorners[1]);
    const bottomSegmentError = pointSegmentDistance(bottomGuide, mappedCorners[3], mappedCorners[2]);
    const rightSegmentError = pointSegmentDistance(rightGuide, mappedCorners[1], mappedCorners[2]);
    const leftSegmentError = pointSegmentDistance(leftGuide, mappedCorners[0], mappedCorners[3]);
    const topMidpointError = Math.abs(topGuide.y - ((mappedCorners[0].y + mappedCorners[1].y) / 2));
    const bottomMidpointError = Math.abs(bottomGuide.y - ((mappedCorners[3].y + mappedCorners[2].y) / 2));
    const rightMidpointError = Math.abs(rightGuide.x - ((mappedCorners[1].x + mappedCorners[2].x) / 2));
    const leftMidpointError = Math.abs(leftGuide.x - ((mappedCorners[0].x + mappedCorners[3].x) / 2));
    const topSlopeError = Math.abs(mappedCorners[0].y - mappedCorners[1].y);
    const bottomSlopeError = Math.abs(mappedCorners[3].y - mappedCorners[2].y);
    const rightSlopeError = Math.abs(mappedCorners[1].x - mappedCorners[2].x);
    const leftSlopeError = Math.abs(mappedCorners[0].x - mappedCorners[3].x);
    return topLineError * 8
      + bottomLineError * 8
      + rightLineError * 8
      + leftLineError * 8
      + topSegmentError * 5
      + bottomSegmentError * 5
      + rightSegmentError * 5
      + leftSegmentError * 5
      + topMidpointError * 6
      + bottomMidpointError * 6
      + rightMidpointError * 6
      + leftMidpointError * 6
      + topSlopeError * 2
      + bottomSlopeError * 2
      + rightSlopeError * 2
      + leftSlopeError * 2
      + Math.abs(scale - 1) * 4;
  };
  const steps = [0.12, 0.06, 0.03, 0.015, 0.008];
  const scaleSteps = [0.4, 0.2, 0.1, 0.05, 0.025];
  for (const step of steps) {
    const scaleStep = scaleSteps[steps.indexOf(step)] ?? 0.025;
    const baseFx = bestFx;
    const baseFy = bestFy;
    const baseScale = bestScale;
    for (let ix = -3; ix <= 3; ix++) {
      for (let iy = -3; iy <= 3; iy++) {
        for (let is = -2; is <= 2; is++) {
          const fx = clamp(baseFx + ix * step, -0.25, 0.25);
          const fy = clamp(baseFy + iy * step, -0.25, 0.25);
          const scale = clamp(baseScale + is * scaleStep, minScale, maxScale);
          const score = scoreCandidate(fx, fy, scale);
          if (score < bestScore) {
            bestScore = score;
            bestFx = fx;
            bestFy = fy;
            bestScale = scale;
          }
        }
      }
    }
  }
  return { fx: bestFx, fy: bestFy, scale: currentScale === 1 ? 1 : bestScale };
}

function formatDeskPoints(points: DeskViewPoint[]): string {
  return `points={[\n${points.map((point) => `  { x: ${point.x.toFixed(4)}, y: ${point.y.toFixed(4)} }`).join(',\n')}\n]}`;
}

const styles: Record<string, React.CSSProperties> = {
  effectPanel: {
    padding: '16px',
    borderRadius: '16px',
    background: 'rgba(2, 6, 23, 0.72)',
    border: '1px solid rgba(56, 189, 248, 0.16)',
    display: 'grid',
    gap: '14px',
  },
  effectHeader: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: '12px',
    color: '#dbeafe',
  },
  controlsRow: {
    display: 'flex',
    gap: '12px',
    flexWrap: 'wrap',
  },
  effectGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
    gap: '14px',
  },
  sliderControl: {
    display: 'grid',
    gap: '8px',
  },
  sliderLabelRow: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: '12px',
  },
  sliderLabel: {
    fontSize: '0.9rem',
    color: '#cbd5e1',
    textTransform: 'uppercase',
    letterSpacing: '0.08em',
  },
  sliderValue: {
    color: '#7dd3fc',
    fontVariantNumeric: 'tabular-nums',
  },
  slider: {
    width: '100%',
    accentColor: '#38bdf8',
  },
  secondaryButton: {
    borderRadius: '12px',
    border: '1px solid rgba(148, 163, 184, 0.28)',
    padding: '12px 16px',
    background: 'rgba(15, 23, 42, 0.9)',
    color: '#cbd5e1',
    fontWeight: 700,
    cursor: 'pointer',
  },
  viewerPanel: {
    display: 'grid',
    gap: '10px',
  },
  viewerHeader: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'baseline',
    gap: '12px',
    color: '#dbeafe',
  },
  viewerMeta: {
    color: '#7dd3fc',
    fontSize: '0.85rem',
  },
  playerShell: {
    position: 'relative',
    aspectRatio: '16 / 9',
    width: '100%',
    overflow: 'hidden',
    borderRadius: '18px',
    background: '#000',
    border: '1px solid rgba(56, 189, 248, 0.2)',
  },
  deskViewShell: {
    position: 'relative',
    width: '100%',
    overflow: 'hidden',
    borderRadius: '18px',
    background: 'transparent',
    border: '1px solid rgba(56, 189, 248, 0.2)',
  },
  hiddenOverlay: {
    position: 'absolute',
    inset: 0,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: '24px',
    background: 'rgba(2, 6, 23, 0.9)',
  },
  hiddenText: {
    color: '#9fb0ca',
    textAlign: 'center',
    fontSize: '0.95rem',
  },
  video: {
    width: '100%',
    height: '100%',
    display: 'block',
    objectFit: 'contain',
    background: '#000',
  },
  stage: {
    position: 'absolute',
    inset: 0,
    pointerEvents: 'auto',
  },
  deskView: {
    width: '100%',
  },
  codeBlock: {
    margin: 0,
    padding: '12px 14px',
    borderRadius: '12px',
    background: 'rgba(2, 6, 23, 0.85)',
    border: '1px solid rgba(56, 189, 248, 0.16)',
    color: '#cbd5e1',
    fontSize: '0.85rem',
    overflowX: 'auto',
  },
};
