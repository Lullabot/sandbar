---
id: 2
group: "review-webapp"
dependencies: [1]
status: "completed"
created: 2026-08-04
model: "sonnet"
effort: "medium"
skills:
  - react
  - vite
---
# Browser client and vite bundle

## Objective

Build the browser half of the review web app: a React entry point that renders
`@self-review/react`'s `ReviewPanel` against a `ReviewAdapter` backed by the
task 1 HTTP endpoints, bundled by vite into `dist/` for the server to serve.

## Skills Required

- **react** — `ReviewPanel`, the `ReviewAdapter` contract, and reading review
  state through a `ReviewPanelHandle` ref.
- **vite** — a production bundle including the library's stylesheet.

## Acceptance Criteria

- [ ] `npm run build` in `roles/self-review/files/webapp/` exits 0 and produces
      `dist/index.html` plus hashed JS and CSS assets; `ls -la dist/` shows them.
- [ ] With the task 1 server running against a scratch git repo containing a
      modified file, a headless browser (`npx playwright screenshot` or
      equivalent) loading `http://127.0.0.1:<n>/` renders the review UI showing
      that file in the file tree, and the screenshot is captured as evidence.
- [ ] The rendered page shows the file's diff content — not an empty state or a
      loading spinner — confirming `loadDiff` reached `/api/diff` successfully.
- [ ] Clicking Finish Review POSTs to `/api/review`; afterwards `review.xml`
      exists in the scratch repo and the server process has exited.
- [ ] The browser console shows no uncaught errors on load (capture console
      output during the headless run).
- [ ] `npx tsc --noEmit` (or the vite build's own type check) reports no type
      errors.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `@self-review/react`'s `ReviewPanel` takes an `adapter` prop; only
  `loadDiff` is required and all other adapter methods are optional and degrade
  gracefully. The optional methods are **out of scope** for this task —
  implement `loadDiff` and the submit path only.
- Import `@self-review/react/styles.css`; the package ships a self-contained
  stylesheet, so **do not** add Tailwind to this app.
- vite with `@vitejs/plugin-react`, building to `dist/` relative to the webapp
  root.
- React 19 / `createRoot`.

## Input Dependencies

From task 1: the pinned `package.json`, the running server, and the
`/api/diff`, `/api/config` and `/api/review` contracts.

## Output Artifacts

- `roles/self-review/files/webapp/src/main.tsx`
- `roles/self-review/files/webapp/index.html`
- `roles/self-review/files/webapp/vite.config.ts`
- A `build` script in `package.json`
- A reproducible `dist/` build (not committed — `dist/` is git-ignored and
  produced in the guest by task 3)

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Copy the embedding pattern from upstream.** The self-review repository's own
`tests/webapp/main.tsx` is the reference implementation of exactly this — a host
app that renders `ReviewPanel` with its own chrome and reads state from a ref.
Clone `https://github.com/e0ipso/self-review` and read that file plus
`tests/webapp/vite.config.ts` before writing anything. The differences in this
app are only that the adapter fetches real data instead of fixtures, and that
finishing the review POSTs instead of stuffing JSON into the DOM.

**The adapter.**

```tsx
const adapter: ReviewAdapter = {
  loadDiff: async () => {
    const res = await fetch('/api/diff');
    if (!res.ok) throw new Error(`/api/diff: ${res.status}`);
    return res.json();
  },
};
```

Do **not** implement `expandContext`, `loadFileContent`, `readAttachment`,
`loadImage`, `changeOutputPath` or `onGuideLoad` — they are explicit v1
non-goals in the plan and the library omits those affordances when absent.

**Config.** Fetch `/api/config` once on mount and pass the result to
`ReviewPanel`'s `config` prop, or supply it through the `ConfigAdapter`'s
`loadConfig` — either is acceptable; prefer whichever upstream's own renderer
does (`src/renderer/App.tsx` in the upstream repo).

**Finishing the review.** Hold a `useRef<ReviewPanelHandle>` and render the
library's `Toolbar` with an `onFinishReview` handler that reads
`reviewRef.current?.getReviewState()` and POSTs it:

```tsx
const state = reviewRef.current?.getReviewState();
if (!state) return;
await fetch('/api/review', {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify(state),
});
```

After a successful POST the server exits, so show a plain "review saved, you can
close this tab" state rather than trying to keep talking to it.

**vite config.** Root is the webapp directory, `build.outDir` is `dist`, base is
`/` (the server serves from the root). Unlike upstream's test harness you must
**not** alias `@self-review/core` — this app never imports core in the browser;
that is the server's job, and core carries Node-only dependencies.

**Verification run.** Start the task 1 server against a temporary git repo with
a modified file, then:

```sh
npx --yes playwright screenshot --wait-for-timeout=3000 \
  http://127.0.0.1:<n>/ /tmp/review-ui.png
```

Confirm the screenshot shows the file tree and diff. Keep the screenshot as
evidence for the phase verification gate.

</details>
