import { useEffect, useMemo, useState } from 'react';
import { type WebRtpInfo, WebRtpProvider, WebRtpStream, useWebRtpStream } from 'react-webrtp';
import { DeskCalibration } from 'react-webrtp/deskview';

const defaultUrl = 'ws://localhost:9687/stream/demoLoop';

export function App() {
  return (
    <WebRtpProvider>
      <AppContent />
    </WebRtpProvider>
  );
}

function AppContent() {
  const [wsUrl, setWsUrl] = useState(defaultUrl);
  const [draftUrl, setDraftUrl] = useState(defaultUrl);
  const [showVideo, setShowVideo] = useState(true);
  const [info, setInfo] = useState<WebRtpInfo>({});
  const [error, setError] = useState('');
  const [lastEvent, setLastEvent] = useState('');
  const stream = useWebRtpStream(wsUrl);

  useEffect(() => {
    if (!stream.client) {
      return;
    }

    const offInfo = stream.client.onInfo((nextInfo) => {
      setInfo((prev) => ({ ...prev, ...nextInfo }));
    });
    const offError = stream.client.onError((nextError) => {
      setError(nextError.message);
    });

    return () => {
      offInfo();
      offError();
    };
  }, [stream.client]);

  const stats = useMemo(
    () => [
      ['Codec', info.codec || '—'],
      ['Frame', info.frameNo ?? '—'],
      ['Resolution', info.width && info.height ? `${info.width}x${info.height}` : '—'],
      ['Dropped', info.dropped ?? 0],
      ['Playing', info.playing ? 'yes' : 'no'],
      ['Last event', lastEvent || '—'],
    ],
    [info, lastEvent],
  );

  return (
    <main style={styles.page}>
      <WebRtpStream url={wsUrl} />
      <section style={styles.panel}>
        <div style={styles.header}>
          <div>
            <h1 style={styles.title}>react-webrtp example</h1>
            <p style={styles.subtitle}>Reusable desk calibration UI with draggable Konva points and a DeskView preview.</p>
          </div>
        </div>

        <label style={styles.label}>
          Stream WebSocket URL
          <div style={styles.controls}>
            <input
              value={draftUrl}
              onChange={(event) => setDraftUrl(event.target.value)}
              placeholder="ws://localhost:8080/stream/camera1"
              style={styles.input}
            />
            <button
              type="button"
              onClick={() => {
                setError('');
                setInfo({});
                setWsUrl(draftUrl.trim());
              }}
              style={styles.button}
            >
              Connect
            </button>
            <button type="button" onClick={() => setShowVideo((prev) => !prev)} style={styles.secondaryButton}>
              {showVideo ? 'Hide video' : 'Show video'}
            </button>
          </div>
        </label>

        <DeskCalibration url={wsUrl} showVideo={showVideo} showCode onEvent={setLastEvent} />

        <div style={styles.metaGrid}>
          {stats.map(([label, value]) => (
            <div key={label} style={styles.metaItem}>
              <span style={styles.metaLabel}>{label}</span>
              <span style={styles.metaValue}>{String(value)}</span>
            </div>
          ))}
        </div>

        {error ? <pre style={styles.error}>{error}</pre> : null}
      </section>
    </main>
  );
}

const styles: Record<string, React.CSSProperties> = {
  page: {
    minHeight: '100vh',
    margin: 0,
    padding: '32px',
    background:
      'radial-gradient(circle at top, rgba(63,94,251,0.16), transparent 30%), linear-gradient(180deg, #0b1020 0%, #111827 100%)',
    color: '#e5edf9',
    fontFamily: 'ui-sans-serif, system-ui, sans-serif',
  },
  panel: {
    maxWidth: '1040px',
    margin: '0 auto',
    padding: '24px',
    border: '1px solid rgba(148, 163, 184, 0.2)',
    borderRadius: '20px',
    background: 'rgba(15, 23, 42, 0.72)',
    backdropFilter: 'blur(18px)',
    boxShadow: '0 24px 80px rgba(0, 0, 0, 0.28)',
    display: 'grid',
    gap: '20px',
  },
  header: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  title: {
    margin: 0,
    fontSize: '2rem',
    lineHeight: 1.1,
  },
  subtitle: {
    margin: '8px 0 0',
    color: '#9fb0ca',
  },
  label: {
    display: 'block',
    fontSize: '0.95rem',
    color: '#cbd5e1',
  },
  controls: {
    display: 'flex',
    gap: '12px',
    marginTop: '8px',
    flexWrap: 'wrap',
  },
  input: {
    flex: '1 1 480px',
    minWidth: '280px',
    borderRadius: '12px',
    border: '1px solid rgba(148, 163, 184, 0.28)',
    padding: '12px 14px',
    background: 'rgba(2, 6, 23, 0.72)',
    color: '#e2e8f0',
    outline: 'none',
  },
  button: {
    borderRadius: '12px',
    border: 'none',
    padding: '12px 18px',
    background: 'linear-gradient(135deg, #38bdf8, #0ea5e9)',
    color: '#082f49',
    fontWeight: 800,
    cursor: 'pointer',
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
  metaGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
    gap: '12px',
  },
  metaItem: {
    padding: '14px',
    borderRadius: '14px',
    background: 'rgba(30, 41, 59, 0.72)',
    border: '1px solid rgba(148, 163, 184, 0.14)',
    display: 'grid',
    gap: '4px',
  },
  metaLabel: {
    fontSize: '0.78rem',
    color: '#94a3b8',
    textTransform: 'uppercase',
    letterSpacing: '0.08em',
  },
  metaValue: {
    fontSize: '1rem',
    color: '#f8fafc',
    fontWeight: 700,
  },
  error: {
    margin: 0,
    padding: '14px',
    borderRadius: '14px',
    background: 'rgba(127, 29, 29, 0.34)',
    border: '1px solid rgba(248, 113, 113, 0.35)',
    color: '#fecaca',
    overflowX: 'auto',
  },
};
