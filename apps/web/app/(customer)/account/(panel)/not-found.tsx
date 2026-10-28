import { StatusPage } from "@/components/status-page";

/** A customer record that does not exist, inside the account shell. */
export default function AccountNotFound() {
  return <StatusPage kind="notFound" />;
}
