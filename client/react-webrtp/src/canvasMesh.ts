export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface MeshPoint {
  x: number;
  y: number;
}

function triangleArea(a: MeshPoint, b: MeshPoint, c: MeshPoint): number {
  return Math.abs((a.x * (b.y - c.y) + b.x * (c.y - a.y) + c.x * (a.y - b.y)) / 2);
}

function triangleTransform(
  srcTri: [MeshPoint, MeshPoint, MeshPoint],
  dstTri: [MeshPoint, MeshPoint, MeshPoint],
): { a: number; b: number; c: number; d: number; e: number; f: number } | null {
  const [s0, s1, s2] = srcTri;
  const [d0, d1, d2] = dstTri;
  const denom = s0.x * (s1.y - s2.y) + s1.x * (s2.y - s0.y) + s2.x * (s0.y - s1.y);
  if (Math.abs(denom) < 1e-6) {
    return null;
  }
  return {
    a: (d0.x * (s1.y - s2.y) + d1.x * (s2.y - s0.y) + d2.x * (s0.y - s1.y)) / denom,
    b: (d0.y * (s1.y - s2.y) + d1.y * (s2.y - s0.y) + d2.y * (s0.y - s1.y)) / denom,
    c: (d0.x * (s2.x - s1.x) + d1.x * (s0.x - s2.x) + d2.x * (s1.x - s0.x)) / denom,
    d: (d0.y * (s2.x - s1.x) + d1.y * (s0.x - s2.x) + d2.y * (s1.x - s0.x)) / denom,
    e:
      (d0.x * (s1.x * s2.y - s2.x * s1.y) +
        d1.x * (s2.x * s0.y - s0.x * s2.y) +
        d2.x * (s0.x * s1.y - s1.x * s0.y)) /
      denom,
    f:
      (d0.y * (s1.x * s2.y - s2.x * s1.y) +
        d1.y * (s2.x * s0.y - s0.x * s2.y) +
        d2.y * (s0.x * s1.y - s1.x * s0.y)) /
      denom,
  };
}

function drawImageTriangle(
  ctx: CanvasRenderingContext2D,
  image: CanvasImageSource,
  srcTri: [MeshPoint, MeshPoint, MeshPoint],
  dstTri: [MeshPoint, MeshPoint, MeshPoint],
): void {
  const transform = triangleTransform(srcTri, dstTri);
  if (!transform || triangleArea(srcTri[0], srcTri[1], srcTri[2]) < 0.5 || triangleArea(dstTri[0], dstTri[1], dstTri[2]) < 0.5) {
    return;
  }
  ctx.save();
  ctx.beginPath();
  ctx.moveTo(dstTri[0].x, dstTri[0].y);
  ctx.lineTo(dstTri[1].x, dstTri[1].y);
  ctx.lineTo(dstTri[2].x, dstTri[2].y);
  ctx.closePath();
  ctx.clip();
  ctx.transform(transform.a, transform.b, transform.c, transform.d, transform.e, transform.f);
  ctx.drawImage(image, 0, 0);
  ctx.restore();
}

function bilerpQuad(quad: [MeshPoint, MeshPoint, MeshPoint, MeshPoint], u: number, v: number): MeshPoint {
  const [p0, p1, p2, p3] = quad;
  const um = 1 - u;
  const vm = 1 - v;
  return {
    x: p0.x * um * vm + p1.x * u * vm + p2.x * u * v + p3.x * um * v,
    y: p0.y * um * vm + p1.y * u * vm + p2.y * u * v + p3.y * um * v,
  };
}

export function drawImageQuadMesh(
  ctx: CanvasRenderingContext2D,
  image: CanvasImageSource,
  srcQuad: MeshPoint[],
  dstRect: Rect,
  stepsX = 24,
  stepsY = 24,
): void {
  if (srcQuad.length !== 4) {
    return;
  }
  const normalizedSrcQuad: [MeshPoint, MeshPoint, MeshPoint, MeshPoint] = [srcQuad[0], srcQuad[1], srcQuad[2], srcQuad[3]];
  const dstQuad: [MeshPoint, MeshPoint, MeshPoint, MeshPoint] = [
    { x: dstRect.x, y: dstRect.y },
    { x: dstRect.x + dstRect.width, y: dstRect.y },
    { x: dstRect.x + dstRect.width, y: dstRect.y + dstRect.height },
    { x: dstRect.x, y: dstRect.y + dstRect.height },
  ];
  const sx = Math.max(1, Math.floor(stepsX));
  const sy = Math.max(1, Math.floor(stepsY));
  for (let y = 0; y < sy; y++) {
    const v0 = y / sy;
    const v1 = (y + 1) / sy;
    for (let x = 0; x < sx; x++) {
      const u0 = x / sx;
      const u1 = (x + 1) / sx;
      const s00 = bilerpQuad(normalizedSrcQuad, u0, v0);
      const s10 = bilerpQuad(normalizedSrcQuad, u1, v0);
      const s11 = bilerpQuad(normalizedSrcQuad, u1, v1);
      const s01 = bilerpQuad(normalizedSrcQuad, u0, v1);
      const d00 = bilerpQuad(dstQuad, u0, v0);
      const d10 = bilerpQuad(dstQuad, u1, v0);
      const d11 = bilerpQuad(dstQuad, u1, v1);
      const d01 = bilerpQuad(dstQuad, u0, v1);
      drawImageTriangle(ctx, image, [s00, s10, s11], [d00, d10, d11]);
      drawImageTriangle(ctx, image, [s00, s11, s01], [d00, d11, d01]);
    }
  }
}

export function drawWarpMesh(
  ctx: CanvasRenderingContext2D,
  image: CanvasImageSource,
  mapPoint: (u: number, v: number) => { src: MeshPoint; dst: MeshPoint },
  stepsX: number,
  stepsY: number,
): void {
  const sx = Math.max(1, Math.floor(stepsX));
  const sy = Math.max(1, Math.floor(stepsY));
  for (let y = 0; y < sy; y++) {
    const v0 = y / sy;
    const v1 = (y + 1) / sy;
    for (let x = 0; x < sx; x++) {
      const u0 = x / sx;
      const u1 = (x + 1) / sx;
      const p00 = mapPoint(u0, v0);
      const p10 = mapPoint(u1, v0);
      const p11 = mapPoint(u1, v1);
      const p01 = mapPoint(u0, v1);
      drawImageTriangle(ctx, image, [p00.src, p10.src, p11.src], [p00.dst, p10.dst, p11.dst]);
      drawImageTriangle(ctx, image, [p00.src, p11.src, p01.src], [p00.dst, p11.dst, p01.dst]);
    }
  }
}
