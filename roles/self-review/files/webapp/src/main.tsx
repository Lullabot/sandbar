import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ReviewPanel, Toolbar } from '@self-review/react';
import type { ReviewPanelHandle } from '@self-review/react';
import type { ReviewAdapter } from '@self-review/react';
import type {
  AppConfig,
  DiffLoadPayload,
  ExpandContextRequest,
  ExpandContextResponse,
} from '@self-review/react';
import '@self-review/react/styles.css';

// Browser half of the sandbar review web app. Mirrors upstream's own
// tests/webapp/main.tsx harness: the host renders its own chrome (Toolbar +
// Finish Review) and reads the completed review through the imperative
// ReviewPanelHandle ref. The only differences from the upstream harness are
// that loadDiff fetches real data from the task 1 server instead of fixtures,
// and finishing a review POSTs to /api/review instead of stuffing JSON into
// the DOM for a test to read.

const adapter: ReviewAdapter = {
  loadDiff: async (): Promise<DiffLoadPayload> => {
    const res = await fetch('/api/diff');
    if (!res.ok) {
      throw new Error(`/api/diff: ${res.status}`);
    }
    return res.json();
  },

  // Not optional in practice, despite being optional in the interface. The
  // panel decides to render its "show all hidden lines" bar from the diff
  // SOURCE (`type === 'git'`, which the server always reports), not from
  // whether the adapter can service it — so leaving this out put an expand
  // control between every pair of hunks on every tracked file, each of which
  // flipped to a loading state on click and then silently did nothing, forever.
  expandContext: async (
    request: ExpandContextRequest
  ): Promise<ExpandContextResponse | null> => {
    const params = new URLSearchParams({
      path: request.filePath,
      contextLines: String(request.contextLines),
    });
    const res = await fetch(`/api/expand?${params}`);
    if (!res.ok) {
      // Null is the interface's "nothing to show" answer; the panel restores
      // the bar rather than hanging on a spinner.
      return null;
    }
    return res.json();
  },
};

type FinishState = 'idle' | 'submitting' | 'done' | 'error';

function App() {
  const reviewRef = useRef<ReviewPanelHandle>(null);
  const [config, setConfig] = useState<Partial<AppConfig> | null>(null);
  const [finishState, setFinishState] = useState<FinishState>('idle');
  const [finishError, setFinishError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch('/api/config')
      .then(res => {
        if (!res.ok) throw new Error(`/api/config: ${res.status}`);
        return res.json();
      })
      .then((data: { config: AppConfig }) => {
        if (!cancelled) setConfig(data.config);
      })
      .catch(() => {
        // Fall back to the library's own defaults rather than blocking
        // the review UI on a config fetch failure.
        if (!cancelled) setConfig({});
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleFinishReview = async () => {
    const state = reviewRef.current?.getReviewState();
    if (!state) return;
    setFinishState('submitting');
    setFinishError(null);
    try {
      // The server exits immediately after responding to this request, so
      // the connection can drop right after the response arrives — that is
      // expected, not a failure, and no further requests should follow.
      const res = await fetch('/api/review', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(state),
      });
      if (!res.ok) {
        throw new Error(`/api/review: ${res.status}`);
      }
      setFinishState('done');
    } catch (err) {
      setFinishState('error');
      setFinishError(err instanceof Error ? err.message : String(err));
    }
  };

  if (config === null) {
    return (
      <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        Loading…
      </div>
    );
  }

  if (finishState === 'done') {
    return (
      <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        Review saved. You can close this tab.
      </div>
    );
  }

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <ReviewPanel
        ref={reviewRef}
        adapter={adapter}
        config={config}
        className="flex-1 flex flex-col overflow-hidden bg-background text-foreground"
      >
        <Toolbar onFinishReview={handleFinishReview} />
      </ReviewPanel>
      {finishState === 'error' && (
        <div style={{ padding: '8px 16px', color: '#e53e3e' }}>
          Failed to save review{finishError ? `: ${finishError}` : ''}.
        </div>
      )}
    </div>
  );
}

const root = createRoot(document.getElementById('root')!);
root.render(<App />);
