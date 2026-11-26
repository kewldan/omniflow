import { ListSkeleton } from "@/components/account/state";

/**
 * What a section shows the moment it is tapped.
 *
 * Every panel route is rendered per request (the CSP nonce and the session
 * cookie make it dynamic), so without a boundary here a tap changed nothing on
 * screen until the server's answer and the page's code had both crossed the
 * network — two round trips to the server before the tab bar even moved, which
 * customers on a phone read as a button that did not work. With it, the router
 * prefetches the shell down to this boundary, commits the navigation at once,
 * and the page streams in behind the placeholder. The shell around it stays
 * mounted, so only the content column changes.
 */
export default function PanelLoading() {
  return <ListSkeleton />;
}
