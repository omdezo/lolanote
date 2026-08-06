import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import jsxA11y from 'eslint-plugin-jsx-a11y';
import reactHooks from 'eslint-plugin-react-hooks';

// There was no linter here at all.
//
// The evidence was sitting in the tree: `// eslint-disable-next-line` comments
// in AgentAsk.tsx and App.tsx, suppressing rules that nothing had run in a very
// long time. Meanwhile every accessibility defect this codebase has — a
// clickable span, clickable divs, a switch with no name, unlabelled inputs — is
// one a static rule catches for free, before anything renders.
//
// Deliberately narrow. This is not a style pass: the recommended TypeScript
// rules are on as a floor, and the accessibility rules are the reason the file
// exists. Widening it into a formatting argument would be how it gets turned
// off again.
export default tseslint.config(
  { ignores: ['dist/**', 'node_modules/**', 'public/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    // react-hooks is registered because the tree already carries
    // `eslint-disable-next-line react-hooks/exhaustive-deps` comments. With no
    // plugin those suppressions referred to a rule that did not exist, which is
    // the clearest possible sign nothing had run here in a long time.
    plugins: { 'jsx-a11y': jsxA11y, 'react-hooks': reactHooks },
    languageOptions: {
      parserOptions: { ecmaFeatures: { jsx: true } },
      globals: {
        window: 'readonly', document: 'readonly', navigator: 'readonly',
        console: 'readonly', fetch: 'readonly', localStorage: 'readonly',
        sessionStorage: 'readonly', setTimeout: 'readonly', clearTimeout: 'readonly',
        setInterval: 'readonly', clearInterval: 'readonly',
        requestAnimationFrame: 'readonly', cancelAnimationFrame: 'readonly',
        WebSocket: 'readonly', HTMLElement: 'readonly', Element: 'readonly',
        Event: 'readonly', MouseEvent: 'readonly', KeyboardEvent: 'readonly',
        File: 'readonly', FileList: 'readonly', Blob: 'readonly', URL: 'readonly',
        FormData: 'readonly', AbortController: 'readonly', crypto: 'readonly',
        Image: 'readonly', ResizeObserver: 'readonly', IntersectionObserver: 'readonly',
        MutationObserver: 'readonly', globalThis: 'readonly', queueMicrotask: 'readonly',
        performance: 'readonly', structuredClone: 'readonly', getComputedStyle: 'readonly',
        SVGSVGElement: 'readonly', HTMLInputElement: 'readonly', HTMLDivElement: 'readonly',
        HTMLTextAreaElement: 'readonly', HTMLCanvasElement: 'readonly',
        HTMLImageElement: 'readonly', HTMLButtonElement: 'readonly',
        DOMParser: 'readonly', TextDecoder: 'readonly', TextEncoder: 'readonly',
        alert: 'readonly', confirm: 'readonly', prompt: 'readonly',
        matchMedia: 'readonly', location: 'readonly', history: 'readonly',
        DragEvent: 'readonly', PointerEvent: 'readonly', WheelEvent: 'readonly',
        ClipboardEvent: 'readonly', TouchEvent: 'readonly', Node: 'readonly',
      },
    },
    rules: {
      // The rules that would have caught the defects this codebase actually
      // has. Errors, not warnings: a warning in a linter nobody runs is a
      // comment.
      'jsx-a11y/alt-text': 'error',
      'jsx-a11y/anchor-has-content': 'error',
      'jsx-a11y/anchor-is-valid': 'error',
      'jsx-a11y/aria-props': 'error',
      'jsx-a11y/aria-proptypes': 'error',
      'jsx-a11y/aria-role': 'error',
      'jsx-a11y/aria-unsupported-elements': 'error',
      'jsx-a11y/heading-has-content': 'error',
      'jsx-a11y/no-redundant-roles': 'error',
      'jsx-a11y/role-has-required-aria-props': 'error',
      'jsx-a11y/role-supports-aria-props': 'error',
      'jsx-a11y/scope': 'error',
      'jsx-a11y/tabindex-no-positive': 'error',
      // The interaction rules are the ones with real work behind them — a
      // clickable div needs a role, a key handler and a tab stop, not a
      // suppression. Warnings for now so the gate lands green and the list is
      // visible; each promotion to error is a deliberate, reviewable step.
      'jsx-a11y/click-events-have-key-events': 'warn',
      'jsx-a11y/no-static-element-interactions': 'warn',
      'jsx-a11y/no-noninteractive-element-interactions': 'warn',
      'jsx-a11y/interactive-supports-focus': 'warn',
      'jsx-a11y/label-has-associated-control': 'warn',

      // TypeScript's own recommended set, minus the two that fight this
      // codebase's deliberate choices rather than finding defects in it.
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': ['error', {
        argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrors: 'none',
      }],
      // `cond ? a() : b()` as a statement is used deliberately here for
      // mutually-exclusive editor commands; it is a style opinion, not a defect.
      '@typescript-eslint/no-unused-expressions': ['error', { allowTernary: true }],
      // The Arabic and Hebrew ranges in lib/direction.ts are character classes
      // whose members are, by definition, unusual codepoints. Flagging them is
      // the rule misfiring on the one file it should not read.
      'no-irregular-whitespace': ['error', { skipRegExps: true }],
    },
  },
);
