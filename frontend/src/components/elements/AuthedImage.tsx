import { useEffect, useState } from 'react';
import { getToken } from '../../auth/keycloak';

/**
 * An <img> for a source that requires the caller to prove who they are.
 *
 * Attachment URLs used to be public blob paths, so a plain <img src> worked.
 * They now point at `/api/v1/attachments/:id/blob`, which asks the permission
 * question on every read — the right design, and the one that makes revoking
 * access actually revoke it.
 *
 * But a browser sends no Authorization header for an <img>. So every image on
 * every board answered 403 and drew the broken-image glyph, with its alt text
 * ("Image with no description") sitting where the picture should be. A security
 * fix that silently deleted every photograph from the product.
 *
 * Fetching with the token and handing the bytes to the element as an object URL
 * keeps the check — each load still asks, each revocation still bites — and
 * gets the picture back. The object URL is revoked on unmount, because a board
 * scrolled through for an hour would otherwise hold every image it ever drew.
 */
export function AuthedImage({ src, alt, className, draggable }: {
  src: string;
  alt: string;
  className?: string;
  draggable?: boolean;
}) {
  const [href, setHref] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    // An absolute URL is somebody else's host (a pasted image, a public
    // bucket). It needs no token and must not be sent one.
    if (!src || /^https?:\/\//i.test(src)) {
      setHref(src || null);
      return;
    }
    let objectURL: string | null = null;
    let cancelled = false;

    void (async () => {
      try {
        const res = await fetch(src, {
          headers: { Authorization: `Bearer ${await getToken()}` },
        });
        if (!res.ok) throw new Error(String(res.status));
        const blob = await res.blob();
        if (cancelled) return;
        objectURL = URL.createObjectURL(blob);
        setHref(objectURL);
      } catch {
        if (!cancelled) setFailed(true);
      }
    })();

    return () => {
      cancelled = true;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [src]);

  // Nothing to draw yet, and nothing to apologise for: a half-loaded board
  // showing broken glyphs reads as damage, while an empty frame reads as
  // loading — which is what it is.
  if (!href) {
    return <div className={className} data-image-state={failed ? 'failed' : 'loading'} aria-hidden={!failed} />;
  }
  return <img src={href} alt={alt} className={className} draggable={draggable} />;
}
