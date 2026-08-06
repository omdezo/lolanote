// The two browser APIs jsdom does not implement that this app reads at module
// scope or on mount. Imported FIRST by component tests, before anything that
// touches them — ES module side effects run in import order, so the position of
// the import is the mechanism.
//
// Both are deliberately minimal. A matchMedia that reported `matches: true`
// would silently flip every component into its coarse-pointer branch and turn
// a suite about desktop behaviour into a suite about touch behaviour without
// saying so; a test that wants the touch branch asks for it explicitly.

interface QueryStub {
  matches: boolean;
  media: string;
  addEventListener(): void;
  removeEventListener(): void;
  addListener(): void;
  removeListener(): void;
  onchange: null;
  dispatchEvent(): boolean;
}

/** Media queries that should report as matching. Empty by default. */
let matching: (query: string) => boolean = () => false;

/** Make `matchMedia` answer true for the queries this test cares about. */
export function stubMatchMedia(predicate: (query: string) => boolean): void {
  matching = predicate;
}

/** Back to "nothing matches" — call between tests that changed it. */
export function resetMatchMedia(): void {
  matching = () => false;
}

if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  window.matchMedia = ((query: string): QueryStub => ({
    matches: matching(query),
    media: query,
    addEventListener() { /* nothing changes under test */ },
    removeEventListener() { /* nothing changes under test */ },
    addListener() { /* deprecated form, still called by some libraries */ },
    removeListener() { /* deprecated form */ },
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

// jsdom has no layout engine and therefore no ResizeObserver; ElementShell
// measures itself with one the moment it mounts.
if (typeof globalThis.ResizeObserver === 'undefined') {
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() { /* nothing to measure without layout */ }
    unobserve() { /* nothing */ }
    disconnect() { /* nothing */ }
  };
}
