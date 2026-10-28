"use client";

import { Button } from "@omniflow/ui/button";
import { StatusNotice } from "@omniflow/ui/empty-state";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTranslations } from "next-intl";

/**
 * The page shown when a route does not exist or failed to render.
 *
 * It replaces the framework's bare default — a black page reading "This page
 * could not be found" with no way back — with the product's own notice and a
 * link to where the visitor most likely meant to be: the operator panel for an
 * /admin address, the account for an /account one, the landing page otherwise.
 */
export function StatusPage({
  kind,
  onRetry,
}: {
  kind: "notFound" | "error";
  /** Offered on a render failure: the error boundary's reset. */
  onRetry?: () => void;
}) {
  const translate = useTranslations("common.status");
  const pathname = usePathname() ?? "/";
  const home = pathname.startsWith("/admin")
    ? { href: "/admin", label: translate("toAdmin") }
    : pathname.startsWith("/account")
      ? { href: "/account", label: translate("toAccount") }
      : { href: "/", label: translate("toHome") };

  return (
    <main className="mx-auto flex min-h-[60dvh] w-full max-w-lg items-center px-4 py-16">
      <StatusNotice
        action={
          <div className="flex flex-wrap justify-center gap-2">
            {onRetry && (
              <Button onClick={onRetry} size="sm">
                {translate("retry")}
              </Button>
            )}
            <Button asChild size="sm" variant={onRetry ? "outline" : "default"}>
              <Link href={home.href}>{home.label}</Link>
            </Button>
          </div>
        }
        className="w-full"
        description={translate(`${kind}.description`)}
        title={translate(`${kind}.title`)}
        variant={kind === "error" ? "danger" : "empty"}
      />
    </main>
  );
}
