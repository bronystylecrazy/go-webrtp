import type { DeskViewPoint } from './DeskView';
import { computeDeskOutputSize as computeDeskOutputSizeBase } from './deskTransform';

export { drawImageQuadMesh, type Rect } from './canvasMesh';
export {
  DeskCalibration,
  type DeskCalibrationHandleRenderProps,
  type DeskCalibrationPoint,
  type DeskCalibrationPointKind,
  type DeskCalibrationProps,
  type DeskCalibrationState,
} from './DeskCalibration';
export { DeskView, type DeskViewHandle, type DeskViewPoint, type DeskViewProps } from './DeskView';
export {
  clamp01,
  displayToSourceTexture,
  sourceToDisplayTexture,
  type DeskTransformPoint,
  type DeskTransformSize,
} from './deskTransform';
export { VERSION } from './version';

export function computeDeskOutputSize(
  width: number,
  height: number,
  points: DeskViewPoint[],
  fx = 0,
  fy = 0,
  scale = 1,
) {
  return computeDeskOutputSizeBase(width, height, points, fx, fy, scale);
}
