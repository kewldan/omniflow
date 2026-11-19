import { LocaleSwitch } from "./locale-switch";

/**
 * The language switch for a screen with no panel chrome around it — the
 * landing page and both sign-in flows — pinned where people look for it.
 */
export function CornerLocaleSwitch() {
  return (
    <div className="fixed top-3 right-3 z-30">
      <LocaleSwitch />
    </div>
  );
}
