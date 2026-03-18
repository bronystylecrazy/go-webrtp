export interface DeskTransformPoint {
  x: number;
  y: number;
}

export interface DeskTransformSize {
  width: number;
  height: number;
}

export function clamp01(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(1, Math.max(0, value));
}

export function textureToClip(point: DeskTransformPoint): { x: number; y: number } {
  return {
    x: clamp01(point.x) * 2 - 1,
    y: 1 - clamp01(point.y) * 2,
  };
}

export function clipToTexture(point: { x: number; y: number }): DeskTransformPoint {
  return {
    x: clamp01((point.x + 1) / 2),
    y: clamp01((1 - point.y) / 2),
  };
}

export function sampleClipFromOutputClip(point: { x: number; y: number }, fx: number, fy: number, scale: number): { x: number; y: number } {
  const safeScale = Number.isFinite(scale) && scale > 0 ? scale : 1;
  let x = Number(point.x || 0);
  let y = Number(point.y || 0);
  x = x + (((y * y) / safeScale) * x / safeScale) * -fx;
  y = y + (((x * x) / safeScale) * y / safeScale) * -fy;
  return {
    x: x / safeScale,
    y: y / safeScale,
  };
}

export function sourceToDisplayTexture(point: DeskTransformPoint, fx: number, fy: number, scale: number): DeskTransformPoint {
  if (!fx && !fy && scale === 1) {
    return {
      x: clamp01(point.x),
      y: clamp01(point.y),
    };
  }
  const targetClip = textureToClip(point);
  let guess = { ...targetClip };
  for (let i = 0; i < 12; i++) {
    const mapped = sampleClipFromOutputClip(guess, fx, fy, scale);
    guess.x += (targetClip.x - mapped.x) * 0.7;
    guess.y += (targetClip.y - mapped.y) * 0.7;
    guess.x = Math.max(-1.2, Math.min(1.2, guess.x));
    guess.y = Math.max(-1.2, Math.min(1.2, guess.y));
  }
  return clipToTexture(guess);
}

export function displayToSourceTexture(point: DeskTransformPoint, fx: number, fy: number, scale: number): DeskTransformPoint {
  if (!fx && !fy && scale === 1) {
    return {
      x: clamp01(point.x),
      y: clamp01(point.y),
    };
  }
  return clipToTexture(sampleClipFromOutputClip(textureToClip(point), fx, fy, scale));
}

export function computeDeskOutputSize(
  sourceWidth: number,
  sourceHeight: number,
  points: DeskTransformPoint[],
  fx: number,
  fy: number,
  scale: number,
): DeskTransformSize {
  const outputPoints = points.map((point) => {
    const mapped = sourceToDisplayTexture(point, fx, fy, scale);
    return {
      x: mapped.x * sourceWidth,
      y: mapped.y * sourceHeight,
    };
  });
  const top = Math.hypot(outputPoints[1].x - outputPoints[0].x, outputPoints[1].y - outputPoints[0].y);
  const bottom = Math.hypot(outputPoints[2].x - outputPoints[3].x, outputPoints[2].y - outputPoints[3].y);
  const left = Math.hypot(outputPoints[3].x - outputPoints[0].x, outputPoints[3].y - outputPoints[0].y);
  const right = Math.hypot(outputPoints[2].x - outputPoints[1].x, outputPoints[2].y - outputPoints[1].y);
  return {
    width: Math.max(80, (top + bottom) / 2),
    height: Math.max(80, (left + right) / 2),
  };
}
