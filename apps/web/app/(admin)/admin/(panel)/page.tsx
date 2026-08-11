"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ArrowRight, ClipboardList, ShieldCheck, UserCog } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";

import { useSession } from "@/lib/session";

/**
 * Panel home.
 *
 * v0.6 delivers the shell and access control, so this page orients the operator
 * around what the foundation actually provides rather than showing placeholder
 * metrics. Operational dashboards arrive with the surfaces that feed them.
 */
export default function AdminHome() {
  const translate = useTranslations("admin");
  const { session, can } = useSession();

  const shortcuts = [
    {
      description: translate("home.cards.operatorsDescription"),
      href: "/admin/operators",
      icon: UserCog,
      key: "operators",
      permission: "admins.read",
      title: translate("navigation.items.operators"),
    },
    {
      description: translate("home.cards.auditDescription"),
      href: "/admin/audit",
      icon: ClipboardList,
      key: "audit",
      permission: "audit.read",
      title: translate("navigation.items.audit"),
    },
    {
      description: translate("home.cards.securityDescription"),
      href: "/admin/security",
      icon: ShieldCheck,
      key: "security",
      title: translate("navigation.items.security"),
    },
  ].filter((shortcut) => !shortcut.permission || can(shortcut.permission));

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-1">
        <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
          {translate("home.eyebrow")}
        </p>
        <h1 className="font-bold text-2xl tracking-tight">
          {translate("home.greeting", { name: session?.account.displayName ?? "" })}
        </h1>
        <p className="max-w-2xl text-muted-foreground text-sm">{translate("home.description")}</p>
      </header>

      <section aria-labelledby="access-heading">
        <Card>
          <CardHeader>
            <CardTitle id="access-heading">{translate("home.accessTitle")}</CardTitle>
            <CardDescription>{translate("home.accessDescription")}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-wrap gap-1.5">
              {session?.account.roles.map((role) => (
                <Badge key={role} variant="solid">
                  {translate(`roles.${role}`)}
                </Badge>
              ))}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {session?.permissions.map((permission) => (
                <Badge key={permission} variant="neutral">
                  <span className="font-mono text-[10px]">{permission}</span>
                </Badge>
              ))}
            </div>
          </CardContent>
        </Card>
      </section>

      <section aria-labelledby="shortcuts-heading" className="flex flex-col gap-3">
        <h2 className="font-semibold text-[15px] tracking-tight" id="shortcuts-heading">
          {translate("home.shortcutsTitle")}
        </h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {shortcuts.map((shortcut) => {
            const Icon = shortcut.icon;
            return (
              <Card key={shortcut.key}>
                <CardHeader>
                  <Icon aria-hidden="true" className="size-5 text-muted-foreground" />
                  <CardTitle>{shortcut.title}</CardTitle>
                  <CardDescription>{shortcut.description}</CardDescription>
                </CardHeader>
                <CardContent>
                  <Button asChild size="sm" variant="outline">
                    <Link href={shortcut.href}>
                      {translate("home.open")}
                      <ArrowRight />
                    </Link>
                  </Button>
                </CardContent>
              </Card>
            );
          })}
        </div>
      </section>
    </div>
  );
}
