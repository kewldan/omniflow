import { StatusPage } from "@/components/status-page";

/** A panel record that does not exist, inside the panel's own chrome. */
export default function PanelNotFound() {
  return <StatusPage kind="notFound" />;
}
