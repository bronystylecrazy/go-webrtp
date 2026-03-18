import React, { createContext, useContext, useEffect, useMemo, useRef, useSyncExternalStore } from 'react';
import { createClient, type WebRtpClient, type WebRtpClientOptions } from './client';

interface StreamEntry {
  client: WebRtpClient;
  options: WebRtpClientOptions;
  refCount: number;
}

class WebRtpRegistry {
  private readonly entries = new Map<string, StreamEntry>();
  private readonly listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = (): number => this.entries.size;

  getClient(url: string): WebRtpClient | null {
    return this.entries.get(url)?.client ?? null;
  }

  retain(url: string, options: WebRtpClientOptions = {}): WebRtpClient | null {
    if (!url) {
      return null;
    }
    let entry = this.entries.get(url);
    if (!entry) {
      entry = {
        client: createClient(url, options),
        options,
        refCount: 0,
      };
      this.entries.set(url, entry);
      this.emit();
    }
    entry.refCount += 1;
    return entry.client;
  }

  release(url: string): void {
    if (!url) {
      return;
    }
    const entry = this.entries.get(url);
    if (!entry) {
      return;
    }
    entry.refCount -= 1;
    if (entry.refCount <= 0) {
      entry.client.close();
      this.entries.delete(url);
      this.emit();
    }
  }

  connect(url: string, options: WebRtpClientOptions = {}): WebRtpClient | null {
    if (!url) {
      return null;
    }
    return this.entries.get(url)?.client ?? this.retain(url, options);
  }

  dispose(url: string): void {
    if (!url) {
      return;
    }
    const entry = this.entries.get(url);
    if (!entry) {
      return;
    }
    entry.client.close();
    this.entries.delete(url);
    this.emit();
  }

  disposeAll(): void {
    for (const entry of this.entries.values()) {
      entry.client.close();
    }
    this.entries.clear();
    this.emit();
  }

  private emit(): void {
    for (const listener of this.listeners) {
      listener();
    }
  }
}

const WebRtpRegistryContext = createContext<WebRtpRegistry | null>(null);

export interface WebRtpProviderProps {
  children?: React.ReactNode;
}

export function WebRtpProvider({ children }: WebRtpProviderProps) {
  const registryRef = useRef<WebRtpRegistry | null>(null);
  if (!registryRef.current) {
    registryRef.current = new WebRtpRegistry();
  }

  useEffect(() => {
    const registry = registryRef.current;
    return () => {
      registry?.disposeAll();
    };
  }, []);

  return <WebRtpRegistryContext.Provider value={registryRef.current}>{children}</WebRtpRegistryContext.Provider>;
}

export interface WebRtpStreamProps extends WebRtpClientOptions {
  url: string;
}

export function WebRtpStream({ url, autoReconnect, reconnectDelayMs, maxReconnectDelayMs, lateFrameThreshold, maxPendingDecode }: WebRtpStreamProps) {
  const registry = useRequiredRegistry();

  useEffect(() => {
    registry.retain(url, {
      autoReconnect,
      reconnectDelayMs,
      maxReconnectDelayMs,
      lateFrameThreshold,
      maxPendingDecode,
    });
    return () => {
      registry.release(url);
    };
  }, [registry, url, autoReconnect, reconnectDelayMs, maxReconnectDelayMs, lateFrameThreshold, maxPendingDecode]);

  return null;
}

export interface UseWebRtpStreamResult {
  url: string;
  src: string;
  client: WebRtpClient | null;
  connected: boolean;
  connect(): WebRtpClient | null;
  dispose(): void;
}

export function useWebRtpStream(url: string, options: WebRtpClientOptions = {}): UseWebRtpStreamResult {
  const registry = useRequiredRegistry();
  useSyncExternalStore(registry.subscribe, registry.getSnapshot, registry.getSnapshot);

  useEffect(() => {
    registry.retain(url, options);
    return () => {
      registry.release(url);
    };
  }, [
    registry,
    url,
    options.autoReconnect,
    options.reconnectDelayMs,
    options.maxReconnectDelayMs,
    options.lateFrameThreshold,
    options.maxPendingDecode,
  ]);

  const client = registry.getClient(url);

  return useMemo(
    () => ({
      url,
      src: url,
      client,
      connected: Boolean(client),
      connect: () => registry.connect(url, options),
      dispose: () => registry.dispose(url),
    }),
    [
      client,
      options.autoReconnect,
      options.reconnectDelayMs,
      options.maxReconnectDelayMs,
      options.lateFrameThreshold,
      options.maxPendingDecode,
      registry,
      url,
    ],
  );
}

function useRequiredRegistry(): WebRtpRegistry {
  const registry = useContext(WebRtpRegistryContext);
  if (!registry) {
    throw new Error('WebRtpProvider is required for WebRtpStream and useWebRtpStream.');
  }
  return registry;
}
