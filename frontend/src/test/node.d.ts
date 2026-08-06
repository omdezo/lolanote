// The two Node primitives the source-text tests need, declared rather than
// pulled in as a dependency.
//
// `?raw` is the obvious alternative and does not work for stylesheets: Vitest
// stubs CSS processing by default, so `import css from './x.css?raw'` resolves
// to an empty string and every assertion over it passes vacuously — a check
// that cannot fail is worse than no check, because it looks like one.
//
// Kept to exactly what is used. This is not a substitute for @types/node; it is
// a statement that two functions exist, in a test-only corner of the tree.
declare module 'node:fs' {
  export function readFileSync(path: string, encoding: 'utf8'): string;
  // The i18n gate walks components/ and canvas/ rather than naming files: a
  // list of files to check is a list somebody forgets to add the next panel to.
  export function readdirSync(path: string): string[];
  export function statSync(path: string): { isDirectory(): boolean };
  // MO10's contract test: metadata that names an icon file is a claim about
  // the disk, and the disk is the only thing that can answer it.
  export function existsSync(path: string): boolean;
}

declare module 'node:path' {
  export function resolve(...segments: string[]): string;
  export function dirname(path: string): string;
}

declare const __dirname: string;
