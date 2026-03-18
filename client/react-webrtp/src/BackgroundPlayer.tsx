import { forwardRef, useEffect, useEffectEvent, useImperativeHandle, useRef } from 'react';
import {
  type WebRtpClient,
  type WebRtpClientOptions,
  type WebRtpErrorCallback,
  type WebRtpEventCallback,
  type WebRtpFrameCallback,
  type WebRtpInfoCallback,
} from './client';
import { useWebRtpStream } from './WebRtpProvider';

export interface BackgroundPlayerHandle {
  getClient(): WebRtpClient | null;
}

export interface BackgroundPlayerProps extends WebRtpClientOptions {
  url: string;
  onInfo?: WebRtpInfoCallback;
  onFrame?: WebRtpFrameCallback;
  onEvent?: WebRtpEventCallback;
  onPlayerError?: WebRtpErrorCallback;
}

export const BackgroundPlayer = forwardRef<BackgroundPlayerHandle, BackgroundPlayerProps>(function BackgroundPlayer(
  {
    url,
    onInfo,
    onFrame,
    onEvent,
    onPlayerError,
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
  },
  ref,
) {
  const clientRef = useRef<WebRtpClient | null>(null);
  const stream = useWebRtpStream(url, {
    autoReconnect,
    reconnectDelayMs,
    maxReconnectDelayMs,
    lateFrameThreshold,
    maxPendingDecode,
  });
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
    const client = stream.client;
    if (!client) {
      return;
    }
    clientRef.current = client;
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
    client.play();

    return () => {
      offInfo();
      offFrame();
      offEvent();
      offError();
      if (clientRef.current === client) {
        clientRef.current = null;
      }
    };
  }, [
    stream.client,
  ]);

  return null;
});
