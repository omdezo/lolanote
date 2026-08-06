// The contract for `content.textPreview`, in one place.
//
// Two writers held incompatible contracts for the same field. The human path
// capped it — NoteCard and DocumentCard both wrote `editor.getText().slice(0,
// 500)` — and the agent path did not, writing up to 20,000 characters straight
// into it. Three consumers read that one field and nothing else: search,
// markdown export, and the agent's own digest.
//
// The corrupting writer turns out to be the person: the agent writes a genuine
// 20,000-character treatment, and the first keystroke a human types into it to
// READ it silently truncates the preview back to 500 — permanently removing
// 97% of the document from search, from every export and from the agent's own
// future reads, while `content.doc` keeps it and only the editor can see it.
//
// The invariant this constant exists to state: **textPreview is a preview.
// Nothing that needs the text may read it.** Fixing the cap does not fix that —
// the real repair is a plain-text projection of `doc` that search, export and
// the agent read instead — but a single agreed cap is the precondition, and it
// makes the disagreement impossible to reintroduce by editing one file.
export const TEXT_PREVIEW_MAX = 500;

/** Cap a plain-text rendering to the preview contract. */
export function toTextPreview(text: string): string {
  return text.slice(0, TEXT_PREVIEW_MAX);
}
