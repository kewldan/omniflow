"use client";

import { useEffect } from "react";

/**
 * Warns before losing unsaved edits.
 *
 * Two escape routes have to be covered and they are not the same mechanism.
 * `beforeunload` handles closing the tab, reloading, or navigating away from the
 * site — the browser owns that prompt and its wording. Clicks on in-app links
 * never reach `beforeunload`, because App Router navigation does not unload the
 * document, so those are intercepted separately in the capture phase, before
 * Next's own link handler runs.
 *
 * The confirmation text for in-app navigation is supplied by the caller so it
 * can be translated; the browser's own dialog cannot be.
 */
export function useUnsavedChanges(dirty: boolean, confirmMessage: string) {
  useEffect(() => {
    if (!dirty) {
      return;
    }

    function onBeforeUnload(event: BeforeUnloadEvent) {
      // Modern browsers ignore any custom string and show their own wording;
      // preventDefault is what actually triggers the prompt.
      event.preventDefault();
      event.returnValue = "";
    }

    function onClickCapture(event: MouseEvent) {
      // Let the browser handle anything that is not a plain left click on a
      // same-tab, same-origin link.
      if (event.defaultPrevented || event.button !== 0) {
        return;
      }
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        return;
      }
      const anchor = (event.target as HTMLElement | null)?.closest?.("a");
      if (!anchor) {
        return;
      }
      const href = anchor.getAttribute("href");
      if (!href || href.startsWith("#") || anchor.target === "_blank") {
        return;
      }
      if (anchor.origin && anchor.origin !== window.location.origin) {
        return;
      }
      if (!window.confirm(confirmMessage)) {
        event.preventDefault();
        event.stopPropagation();
      }
    }

    window.addEventListener("beforeunload", onBeforeUnload);
    // Capture phase: Next's Link listens on bubble, so this runs first and can
    // still cancel the navigation.
    document.addEventListener("click", onClickCapture, true);
    return () => {
      window.removeEventListener("beforeunload", onBeforeUnload);
      document.removeEventListener("click", onClickCapture, true);
    };
  }, [confirmMessage, dirty]);
}
