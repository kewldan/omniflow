"use client";

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@omniflow/ui/breadcrumb";
import { Button } from "@omniflow/ui/button";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@omniflow/ui/sheet";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Menu } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { type ReactNode, useEffect, useState } from "react";

import { breadcrumbTrail } from "@/lib/navigation";
import { useSession } from "@/lib/session";

import { AdminSidebar } from "./admin-sidebar";
import { CommandMenu } from "./command-menu";
import { StateNotice } from "./state-notice";
import { UserMenu } from "./user-menu";

/**
 * The authenticated panel frame.
 *
 * It owns the four states every operator page shares — loading, signed out,
 * unreachable, and ready — so an individual page only has to handle its own
 * data. A signed-out session redirects rather than rendering an empty shell.
 */
export function AdminShell({ children }: { children: ReactNode }) {
  const { loading, signedOut, error, stale } = useSession();
  const translate = useTranslations("admin");
  const router = useRouter();
  const pathname = usePathname();
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    if (signedOut) {
      // `next` brings the operator back where they were once they sign in.
      router.replace(`/admin/login?next=${encodeURIComponent(pathname)}`);
    }
  }, [pathname, router, signedOut]);

  if (loading || signedOut) {
    return (
      <div aria-busy="true" className="flex min-h-dvh flex-col gap-4 p-6">
        <span className="sr-only">{translate("states.loading")}</span>
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex min-h-dvh items-center justify-center p-6">
        <StateNotice
          action={
            <Button onClick={() => router.refresh()} size="sm" variant="outline">
              {translate("states.retry")}
            </Button>
          }
          description={translate("states.sessionErrorDescription")}
          title={translate("states.sessionError")}
          variant="danger"
        />
      </div>
    );
  }

  const trail = breadcrumbTrail(pathname);

  return (
    <div className="flex min-h-dvh bg-background">
      {/* Persistent rail on desktop; a sheet on narrow screens. */}
      <aside className="hidden w-60 shrink-0 border-border border-r lg:block">
        <div className="flex h-14 items-center gap-2 border-border border-b px-4">
          <Link className="font-semibold text-[15px] tracking-tight" href="/admin">
            Omniflow
          </Link>
          <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
            {translate("panelBadge")}
          </span>
        </div>
        <AdminSidebar />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-border border-b bg-chrome/85 px-4 backdrop-blur">
          <Sheet onOpenChange={setMobileOpen} open={mobileOpen}>
            <SheetTrigger asChild>
              <Button
                aria-label={translate("navigation.open")}
                className="lg:hidden"
                size="icon-sm"
                variant="ghost"
              >
                <Menu />
              </Button>
            </SheetTrigger>
            <SheetContent className="w-64 p-0" side="left">
              <SheetTitle className="px-4 pt-4">Omniflow</SheetTitle>
              <AdminSidebar onNavigate={() => setMobileOpen(false)} />
            </SheetContent>
          </Sheet>

          <Breadcrumb className="min-w-0 flex-1">
            <BreadcrumbList>
              <BreadcrumbItem>
                {trail.length === 0 ? (
                  <BreadcrumbPage>{translate("navigation.items.dashboard")}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink asChild>
                    <Link href="/admin">{translate("navigation.items.dashboard")}</Link>
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
              {trail.map((item) => (
                <BreadcrumbItem key={item.key}>
                  <BreadcrumbSeparator />
                  <BreadcrumbPage>{translate(`navigation.items.${item.key}`)}</BreadcrumbPage>
                </BreadcrumbItem>
              ))}
            </BreadcrumbList>
          </Breadcrumb>

          <div className="hidden md:block">
            <CommandMenu />
          </div>
          <UserMenu />
        </header>

        {/*
          A background revalidation keeps the current data on screen and marks
          it stale, rather than replacing a usable page with a spinner.
        */}
        <main className="min-w-0 flex-1 p-4 sm:p-6" data-stale={stale || undefined}>
          <div className="mx-auto w-full max-w-6xl">{children}</div>
        </main>
      </div>
    </div>
  );
}
