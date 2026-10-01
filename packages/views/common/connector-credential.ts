import { useEffect, useRef } from "react";

/** Bearer and Personal Access Token rules shared with the server: 1..4096
 * characters, no CR, LF or NUL. */
export const MAX_BEARER_LENGTH = 4096;

export function isValidBearer(value: string): boolean {
  return value.length > 0 && value.length <= MAX_BEARER_LENGTH && !/[\r\n\0]/.test(value);
}

/** Coming back from a provider sign-in can restore the page from the
 * back/forward cache with its "redirecting" state intact; clear it then so
 * the connect button works again. */
export function useResetOnBackForwardRestore(active: boolean, reset: () => void) {
  const resetRef = useRef(reset);
  resetRef.current = reset;
  useEffect(() => {
    if (!active) return;
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) resetRef.current();
    };
    window.addEventListener("pageshow", onPageShow);
    return () => window.removeEventListener("pageshow", onPageShow);
  }, [active]);
}
