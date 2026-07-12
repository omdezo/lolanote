// QomraNote icon set — hand-tuned 24×24 stroke glyphs in the SF Symbols
// spirit: 1.7px rounded strokes, optical centering, currentColor. One import
// site, zero emoji.
import type { SVGProps } from 'react';

type P = SVGProps<SVGSVGElement> & { size?: number };

function base({ size = 20, ...rest }: P) {
  return {
    width: size,
    height: size,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.7,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    ...rest,
  };
}

export const NoteIcon = (p: P) => (
  <svg {...base(p)}><rect x="4.5" y="4" width="15" height="16" rx="2.5" /><path d="M8.5 9h7M8.5 12.5h7M8.5 16h4" /></svg>
);
export const LinkIcon = (p: P) => (
  <svg {...base(p)}><path d="M10 14.5 14 9.5" /><path d="M8.5 10.5 6.6 12.8a3.4 3.4 0 0 0 5.2 4.4l1.9-2.3" /><path d="M15.5 13.5l1.9-2.3a3.4 3.4 0 0 0-5.2-4.4L10.3 9" /></svg>
);
export const TodoIcon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 6.7 6 8.2 9 5.2" /><path d="M12 6.7h7.5" /><path d="M4.5 13.2 6 14.7 9 11.7" /><path d="M12 13.2h7.5" /><path d="M12 19.7h7.5" /><circle cx="6" cy="19.7" r="1" fill="currentColor" stroke="none" /></svg>
);
export const LineIcon = (p: P) => (
  <svg {...base(p)}><path d="M5.5 18.5 18.5 5.5" /><path d="M12 5.5h6.5V12" /></svg>
);
export const BoardIcon = (p: P) => (
  <svg {...base(p)}><rect x="4" y="4" width="7" height="7" rx="1.8" /><rect x="13" y="4" width="7" height="7" rx="1.8" /><rect x="4" y="13" width="7" height="7" rx="1.8" /><rect x="13" y="13" width="7" height="7" rx="1.8" /></svg>
);
export const ColumnIcon = (p: P) => (
  <svg {...base(p)}><rect x="6.5" y="4" width="11" height="16" rx="2" /><path d="M9.5 8h5M9.5 11.5h5M9.5 15h3" /></svg>
);
export const CommentIcon = (p: P) => (
  <svg {...base(p)}><path d="M20 11.5a7.5 7.5 0 0 1-11 6.6L4.5 19.5l1.4-4.2A7.5 7.5 0 1 1 20 11.5Z" /></svg>
);
export const TableIcon = (p: P) => (
  <svg {...base(p)}><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M4 10h16M4 14.5h16M10 10v9M15.5 10v9" /></svg>
);
export const MoreIcon = (p: P) => (
  <svg {...base(p)}><circle cx="6" cy="12" r="1.2" fill="currentColor" stroke="none" /><circle cx="12" cy="12" r="1.2" fill="currentColor" stroke="none" /><circle cx="18" cy="12" r="1.2" fill="currentColor" stroke="none" /></svg>
);
export const SketchIcon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 17.5c2.5-6 5-9.5 6.7-8.3 1.6 1.1-2.8 7 .1 7.1 2 .1 3.4-3.6 5.2-3.3 1.4.2 1.6 2 3 2" /></svg>
);
export const ColorIcon = (p: P) => (
  <svg {...base(p)}><path d="M12 4.5s5.5 6.1 5.5 10a5.5 5.5 0 0 1-11 0c0-3.9 5.5-10 5.5-10Z" /></svg>
);
export const DocumentIcon = (p: P) => (
  <svg {...base(p)}><path d="M7 3.8h7L18.8 8.6V20a1.7 1.7 0 0 1-1.7 1.7H7A1.7 1.7 0 0 1 5.3 20V5.5A1.7 1.7 0 0 1 7 3.8Z" /><path d="M14 4v5h5" /><path d="M8.5 13h7M8.5 16.5h5" /></svg>
);
export const AudioIcon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 10.5v3M8 8v8M11.5 5.5v13M15 8.5v7M18.5 10.5v3" /></svg>
);
export const MapIcon = (p: P) => (
  <svg {...base(p)}><path d="M12 21s-6.5-6-6.5-10.6a6.5 6.5 0 0 1 13 0C18.5 15 12 21 12 21Z" /><circle cx="12" cy="10.2" r="2.2" /></svg>
);
export const VideoIcon = (p: P) => (
  <svg {...base(p)}><rect x="4" y="5.5" width="16" height="13" rx="2.5" /><path d="m10.5 9.5 4.5 2.5-4.5 2.5v-5Z" fill="currentColor" stroke="none" /></svg>
);
export const HeadingIcon = (p: P) => (
  <svg {...base(p)}><path d="M5.5 5.5h13M12 5.5v13" /><path d="M9 18.5h6" /></svg>
);
export const ImageIcon = (p: P) => (
  <svg {...base(p)}><rect x="4" y="5" width="16" height="14" rx="2.5" /><circle cx="9" cy="10" r="1.6" /><path d="m5 17 4.5-4.5c.6-.6 1.5-.6 2.1 0L16 17M14.5 15l1.6-1.6c.6-.6 1.5-.6 2.1 0L20 15" /></svg>
);
export const UploadIcon = (p: P) => (
  <svg {...base(p)}><path d="M12 15V4.5M8 8.5l4-4 4 4" /><path d="M5 15.5v3A1.5 1.5 0 0 0 6.5 20h11a1.5 1.5 0 0 0 1.5-1.5v-3" /></svg>
);
export const DrawIcon = (p: P) => (
  <svg {...base(p)}><path d="m14.5 5.5 4 4L8 20l-4.6.6L4 16 14.5 5.5Z" /><path d="m12.5 7.5 4 4" /></svg>
);
export const TrashIcon = (p: P) => (
  <svg {...base(p)}><path d="M5 7h14M10 4.5h4M8.5 7l.7 12a1.6 1.6 0 0 0 1.6 1.5h2.4a1.6 1.6 0 0 0 1.6-1.5L15.5 7" /><path d="M10.2 10.5v6M13.8 10.5v6" /></svg>
);
export const SearchIcon = (p: P) => (
  <svg {...base(p)}><circle cx="11" cy="11" r="6" /><path d="m19.5 19.5-4.2-4.2" /></svg>
);
export const InboxIcon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 13.5 7 6.5a1.5 1.5 0 0 1 1.4-1h7.2a1.5 1.5 0 0 1 1.4 1l2.5 7" /><path d="M4.5 13.5h4.2c.5 0 .9.3 1.1.7l.4.8c.2.4.6.7 1.1.7h1.4c.5 0 .9-.3 1.1-.7l.4-.8c.2-.4.6-.7 1.1-.7h4.2V17a2.3 2.3 0 0 1-2.3 2.3H6.8A2.3 2.3 0 0 1 4.5 17v-3.5Z" /></svg>
);
export const ShareIcon = (p: P) => (
  <svg {...base(p)}><circle cx="9" cy="9" r="3.2" /><path d="M4 19.5c.6-3 2.7-4.5 5-4.5 1.2 0 2.4.4 3.3 1.2" /><path d="M17 8.5v6M14 11.5h6" /></svg>
);
export const ExportIcon = (p: P) => (
  <svg {...base(p)}><path d="M12 4.5V15M8 11.5l4 4 4-4" /><path d="M5 19.5h14" /></svg>
);
export const UndoIcon = (p: P) => (
  <svg {...base(p)}><path d="M8 6.5 4.5 10 8 13.5" /><path d="M4.5 10h9a5 5 0 0 1 0 10H9" /></svg>
);
export const RedoIcon = (p: P) => (
  <svg {...base(p)}><path d="m16 6.5 3.5 3.5L16 13.5" /><path d="M19.5 10h-9a5 5 0 0 0 0 10H15" /></svg>
);
export const LogoutIcon = (p: P) => (
  <svg {...base(p)}><path d="M14.5 4.5H7A1.5 1.5 0 0 0 5.5 6v12A1.5 1.5 0 0 0 7 19.5h7.5" /><path d="M15 8.5l3.5 3.5L15 15.5M18.5 12h-8" /></svg>
);
export const CloseIcon = (p: P) => (
  <svg {...base(p)}><path d="m6.5 6.5 11 11M17.5 6.5l-11 11" /></svg>
);
export const PlusIcon = (p: P) => (
  <svg {...base(p)}><path d="M12 5.5v13M5.5 12h13" /></svg>
);
export const MinusIcon = (p: P) => (
  <svg {...base(p)}><path d="M5.5 12h13" /></svg>
);
export const CheckIcon = (p: P) => (
  <svg {...base(p)}><path d="m5 12.5 4.5 4.5L19 7.5" /></svg>
);
export const DuplicateIcon = (p: P) => (
  <svg {...base(p)}><rect x="8.5" y="8.5" width="11" height="11" rx="2" /><path d="M5.5 15.5A1.5 1.5 0 0 1 4 14V6a1.5 1.5 0 0 1 1.5-1.5H14A1.5 1.5 0 0 1 15.5 6" /></svg>
);
export const SyncIcon = (p: P) => (
  <svg {...base(p)}><path d="M19 5.5V10h-4.5" /><path d="M19 10a7.3 7.3 0 0 0-13.2-2.5M5 18.5V14h4.5" /><path d="M5 14a7.3 7.3 0 0 0 13.2 2.5" /></svg>
);
export const ChevronIcon = (p: P) => (
  <svg {...base(p)}><path d="m9.5 6 6 6-6 6" /></svg>
);
export const FitIcon = (p: P) => (
  <svg {...base(p)}><path d="M9 4.5H6A1.5 1.5 0 0 0 4.5 6v3M15 4.5h3A1.5 1.5 0 0 1 19.5 6v3M9 19.5H6A1.5 1.5 0 0 1 4.5 18v-3M15 19.5h3a1.5 1.5 0 0 0 1.5-1.5v-3" /></svg>
);
export const RestoreIcon = (p: P) => (
  <svg {...base(p)}><path d="M5 9.5V5m0 4.5H9.5" /><path d="M5.2 9.3A8 8 0 1 1 4 12" /></svg>
);
export const BoardGlyph = (p: P) => (
  <svg {...base({ strokeWidth: 2, ...p } as P)}><rect x="4" y="4" width="7" height="7" rx="2" /><rect x="13" y="4" width="7" height="7" rx="2" /><rect x="4" y="13" width="7" height="7" rx="2" /><rect x="13" y="13" width="7" height="7" rx="2" /></svg>
);
export const AliasArrow = (p: P) => (
  <svg {...base(p)}><path d="M7 17 17 7M10 7h7v7" /></svg>
);
export const FileIcon = (p: P) => (
  <svg {...base(p)}><path d="M7 3.8h7L18.8 8.6V20a1.7 1.7 0 0 1-1.7 1.7H7A1.7 1.7 0 0 1 5.3 20V5.5A1.7 1.7 0 0 1 7 3.8Z" /><path d="M14 4v5h5" /></svg>
);
export const BellIcon = (p: P) => (
  <svg {...base(p)}><path d="M6 16.5v-5a6 6 0 0 1 12 0v5l1.5 2.5h-15L6 16.5Z" /><path d="M10 21.5a2.2 2.2 0 0 0 4 0" /></svg>
);
export const LockIcon = (p: P) => (
  <svg {...base(p)}><rect x="5.5" y="10.5" width="13" height="9" rx="2" /><path d="M8 10.5V8a4 4 0 0 1 8 0v2.5" /><circle cx="12" cy="15" r="1.2" fill="currentColor" stroke="none" /></svg>
);
export const TemplateIcon = (p: P) => (
  <svg {...base(p)}><rect x="4" y="4" width="16" height="16" rx="2.5" /><path d="M4 9h16M9 9v11" /></svg>
);
export const LabelIcon = (p: P) => (
  <svg {...base(p)}><path d="M4 8.5A2 2 0 0 1 6 6.5h6.5l6.5 5.5-6.5 5.5H6a2 2 0 0 1-2-2v-7Z" /><circle cx="8" cy="12" r="1.2" fill="currentColor" stroke="none" /></svg>
);
export const HomeIcon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 11 12 4.5l7.5 6.5" /><path d="M6.5 9.7V19a1 1 0 0 0 1 1H16.5a1 1 0 0 0 1-1V9.7" /></svg>
);
export const BoldIcon = (p: P) => (
  <svg {...base({ strokeWidth: 2.1, ...p } as P)}><path d="M7.5 5.5h5a3 3 0 0 1 0 6h-5v-6ZM7.5 11.5h6a3.2 3.2 0 0 1 0 6.4h-6v-6.4Z" /></svg>
);
export const ItalicIcon = (p: P) => (
  <svg {...base(p)}><path d="M10.5 5.5h7M6.5 18.5h7M14 5.5l-4 13" /></svg>
);
export const StrikeIcon = (p: P) => (
  <svg {...base(p)}><path d="M6 12h12" /><path d="M16.5 7.5c-.6-1.4-2.3-2.3-4.4-2.3-2.5 0-4.3 1.2-4.3 3.1 0 .7.2 1.2.6 1.7M7.8 16c.6 1.6 2.4 2.7 4.6 2.7 2.6 0 4.3-1.3 4.3-3.2 0-.5-.1-1-.3-1.4" /></svg>
);
export const CodeIcon = (p: P) => (
  <svg {...base(p)}><path d="m8.5 8-4 4 4 4M15.5 8l4 4-4 4" /></svg>
);
export const QuoteIcon = (p: P) => (
  <svg {...base(p)}><path d="M9.5 7.5c-2.6.8-4 2.6-4 5.2V17h4.6v-4.6H7.3c0-1.6.9-2.8 2.2-3.4V7.5ZM18.5 7.5c-2.6.8-4 2.6-4 5.2V17h4.6v-4.6h-2.8c0-1.6.9-2.8 2.2-3.4V7.5Z" fill="currentColor" stroke="none" /></svg>
);
export const ListIcon = (p: P) => (
  <svg {...base(p)}><path d="M9.5 6.5h10M9.5 12h10M9.5 17.5h10" /><circle cx="5.3" cy="6.5" r="1.1" fill="currentColor" stroke="none" /><circle cx="5.3" cy="12" r="1.1" fill="currentColor" stroke="none" /><circle cx="5.3" cy="17.5" r="1.1" fill="currentColor" stroke="none" /></svg>
);
export const H1Icon = (p: P) => (
  <svg {...base(p)}><path d="M4.5 6v12M11 6v12M4.5 12H11" /><path d="M16 9.5l2.5-1.5V18" /></svg>
);
export const H2Icon = (p: P) => (
  <svg {...base(p)}><path d="M4 6.5v11M10 6.5v11M4 12h6" /><path d="M14.5 10.2a2.6 2.6 0 0 1 5.1.7c0 2.5-5 4-5 6.6h5.2" /></svg>
);
export const EmptyTrayIllustration = (p: P) => (
  <svg {...base({ size: 56, strokeWidth: 1.2, ...p } as P)}><path d="M4.5 13.5 7 6.5a1.5 1.5 0 0 1 1.4-1h7.2a1.5 1.5 0 0 1 1.4 1l2.5 7" opacity="0.5" /><path d="M4.5 13.5h4.2c.5 0 .9.3 1.1.7l.4.8c.2.4.6.7 1.1.7h1.4c.5 0 .9-.3 1.1-.7l.4-.8c.2-.4.6-.7 1.1-.7h4.2V17a2.3 2.3 0 0 1-2.3 2.3H6.8A2.3 2.3 0 0 1 4.5 17v-3.5Z" opacity="0.5" /></svg>
);
