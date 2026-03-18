import React, { forwardRef, useEffect, useEffectEvent, useImperativeHandle, useRef } from 'react';
import {
  type WebRtpClient,
  type WebRtpClientOptions,
  type WebRtpErrorCallback,
  type WebRtpEventCallback,
  type WebRtpFrameCallback,
  type WebRtpInfoCallback,
} from './client';
import { useWebRtpStream } from './WebRtpProvider';

export interface VideoPlayerHandle {
  getClient(): WebRtpClient | null;
}

export interface VideoPlayerProps
  extends Omit<React.VideoHTMLAttributes<HTMLVideoElement>, 'src' | 'srcObject' | 'onError'>,
    WebRtpClientOptions {
  url: string;
  calibration?: React.ReactNode;
  onInfo?: WebRtpInfoCallback;
  onFrame?: WebRtpFrameCallback;
  onEvent?: WebRtpEventCallback;
  onPlayerError?: WebRtpErrorCallback;
}

export const VideoPlayer = forwardRef<VideoPlayerHandle, VideoPlayerProps>(function VideoPlayer(
  {
    url,
    calibration,
    onInfo,
    onFrame,
    onEvent,
    onPlayerError,
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
    autoPlay = true,
    muted = true,
    playsInline = true,
    ...videoProps
  },
  ref,
) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const clientRef = useRef<WebRtpClient | null>(null);
  const stream = useWebRtpStream(url, {
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
  });
  const renderClient = stream.client;
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

  useImperativeHandle(
    ref,
    () => ({
      getClient: () => clientRef.current,
    }),
    [],
  );

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }

    const client = stream.client;
    if (!client) {
      return;
    }
    clientRef.current = client;
    client.render(video);
    const offInfo = client.onInfo((nextInfo) => {
      handleInfo(nextInfo);
    });
    const offFrame = client.onFrame((frameNo, data, isKey) => {
      handleFrame(frameNo, data, isKey);
    });
    const offEvent = client.onEvent((nextEvent) => {
      handleEvent(nextEvent);
    });
    const offError = client.onError((nextError) => {
      handlePlayerError(nextError);
    });
    if (autoPlay) {
      client.play();
    }

    return () => {
      offInfo();
      offFrame();
      offEvent();
      offError();
      client.detach();
      if (clientRef.current === client) {
        clientRef.current = null;
      }
    };
  }, [
    stream.client,
    autoPlay,
  ]);

  const calibrationNode = React.isValidElement(calibration)
    ? React.cloneElement(calibration, {
        url,
        overlayOnly: true,
      } as Record<string, unknown>)
    : calibration;

  if (!calibrationNode) {
    return <video ref={videoRef} autoPlay={autoPlay} muted={muted} playsInline={playsInline} {...videoProps} />;
  }

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      <video ref={videoRef} autoPlay={autoPlay} muted={muted} playsInline={playsInline} {...videoProps} />
      {calibrationNode}
    </div>
  );
});
