/**
 * The installation settings sections and the fields each one holds.
 *
 * The schema is declared here rather than as nine hand-written forms because
 * the sections differ in content and not in shape: every one is a document plus
 * an optional set of write-only secrets, saved with a version guard. One
 * renderer over a declaration means a new field is one line, and it means the
 * secret handling cannot be got right in eight places and wrong in the ninth.
 *
 * A field marked `secret` is never populated from the server — the API does not
 * return one — and an empty secret field on save leaves the stored value alone.
 */

export type FieldKind = "text" | "url" | "number" | "boolean" | "textarea" | "secret";

export type SectionField = {
  /** Key inside the section's document, or inside its secrets when `secret`. */
  name: string;
  kind: FieldKind;
  /** Suffix under `admin.installationSettings.fields` for the label and hint. */
  messageKey: string;
};

/**
 * The areas settings are split across, one route each.
 *
 * They exist because everything used to be one page: commerce, ten
 * installation sections, the telemetry preview, the backup history, the
 * diagnostics bundle, and every customer sign-in provider, stacked vertically.
 * Finding the maintenance notice meant scrolling past the payment providers,
 * and the page fired a request for all of it whether or not the operator was
 * looking at any of it.
 *
 * The grouping is by the question an operator arrived with, not by which table
 * the value lands in: "how do we connect to things", "what happens on its own",
 * "how do we look and who may sign in".
 */
export type SettingsGroupKey = "commerce" | "integrations" | "operations" | "brand";

export type SettingsGroup = {
  key: SettingsGroupKey | "signIn" | "ai" | "connect" | "notices" | "analytics";
  href: string;
  /** The permission the route itself requires. */
  permission: string;
};

export const SETTINGS_GROUPS: SettingsGroup[] = [
  { key: "commerce", href: "/admin/settings/commerce", permission: "settings.read" },
  { key: "integrations", href: "/admin/settings/integrations", permission: "settings.read" },
  { key: "operations", href: "/admin/settings/operations", permission: "settings.read" },
  { key: "brand", href: "/admin/settings/brand", permission: "settings.read" },
  { key: "connect", href: "/admin/settings/connect", permission: "settings.read" },
  // The wording of the messages the installation sends on its own initiative.
  // It sits here rather than under marketing because there is no audience
  // decision to make: these reach every customer, and how the installation
  // speaks is a configuration of it rather than a campaign.
  { key: "notices", href: "/admin/settings/notices", permission: "settings.read" },
  // The operator's own advertising measurement, off by default and never
  // project telemetry. It is a settings area rather than a marketing one
  // because there is no audience decision in it.
  { key: "analytics", href: "/admin/settings/analytics", permission: "settings.read" },
  { key: "signIn", href: "/admin/settings/sign-in", permission: "settings.read" },
  { key: "ai", href: "/admin/settings/ai", permission: "settings.read" },
];

export type SectionSchema = {
  section: string;
  /** Which route the section is rendered on. */
  group: SettingsGroupKey;
  /** Suffix under `admin.installationSettings.sections`. */
  messageKey: string;
  fields: SectionField[];
};

/** The sections one route renders, in the order they are declared. */
export function sectionsInGroup(group: SettingsGroupKey): SectionSchema[] {
  return SECTIONS.filter((schema) => schema.group === group);
}

export const SECTIONS: SectionSchema[] = [
  {
    section: "branding",
    group: "brand",
    messageKey: "branding",
    fields: [
      { name: "serviceName", kind: "text", messageKey: "serviceName" },
      { name: "supportContact", kind: "text", messageKey: "supportContact" },
      { name: "publicUrl", kind: "url", messageKey: "publicUrl" },
      { name: "termsUrl", kind: "url", messageKey: "termsUrl" },
      { name: "defaultLocale", kind: "text", messageKey: "defaultLocale" },
      { name: "timezone", kind: "text", messageKey: "timezone" },
    ],
  },
  {
    section: "remnawave",
    group: "integrations",
    messageKey: "remnawave",
    fields: [
      { name: "baseUrl", kind: "url", messageKey: "remnawaveBaseUrl" },
      { name: "reconcileIntervalMinutes", kind: "number", messageKey: "reconcileInterval" },
      { name: "compatibilityChecked", kind: "boolean", messageKey: "compatibilityChecked" },
      // The token is write-only. Rotating it means typing a new one; leaving
      // this empty keeps the current one, which is what makes rotation a
      // deliberate act rather than a side effect of editing the base URL.
      { name: "token", kind: "secret", messageKey: "remnawaveToken" },
    ],
  },
  {
    section: "telegram",
    group: "integrations",
    messageKey: "telegram",
    fields: [
      { name: "botUsername", kind: "text", messageKey: "botUsername" },
      { name: "webhookUrl", kind: "url", messageKey: "webhookUrl" },
      { name: "webhookEnabled", kind: "boolean", messageKey: "webhookEnabled" },
      { name: "botToken", kind: "secret", messageKey: "botToken" },
      { name: "webhookSecret", kind: "secret", messageKey: "webhookSecret" },
    ],
  },
  {
    section: "operator_group",
    group: "integrations",
    messageKey: "operatorGroup",
    fields: [
      { name: "chatId", kind: "text", messageKey: "operatorChatId" },
      { name: "createTopics", kind: "boolean", messageKey: "createTopics" },
      { name: "notificationCap", kind: "number", messageKey: "notificationCap" },
      { name: "windowMinutes", kind: "number", messageKey: "windowMinutes" },
    ],
  },
  {
    section: "required_channels",
    group: "integrations",
    messageKey: "requiredChannels",
    fields: [
      { name: "recheckIntervalMinutes", kind: "number", messageKey: "recheckInterval" },
      { name: "graceHours", kind: "number", messageKey: "graceHours" },
      { name: "warnBeforeSuspend", kind: "boolean", messageKey: "warnBeforeSuspend" },
    ],
  },
  {
    section: "maintenance",
    group: "operations",
    messageKey: "maintenance",
    fields: [
      { name: "autoEnable", kind: "boolean", messageKey: "autoEnable" },
      { name: "failureThreshold", kind: "number", messageKey: "failureThreshold" },
      { name: "noticeEn", kind: "textarea", messageKey: "noticeEn" },
      { name: "noticeRu", kind: "textarea", messageKey: "noticeRu" },
    ],
  },
  {
    section: "notifications",
    group: "operations",
    messageKey: "notifications",
    fields: [
      { name: "failedPaymentThreshold", kind: "number", messageKey: "failedPaymentThreshold" },
      { name: "driftThreshold", kind: "number", messageKey: "driftThreshold" },
      { name: "outboxBacklogThreshold", kind: "number", messageKey: "outboxThreshold" },
      { name: "quietHoursStart", kind: "text", messageKey: "quietHoursStart" },
      { name: "quietHoursEnd", kind: "text", messageKey: "quietHoursEnd" },
    ],
  },
  {
    section: "telemetry",
    group: "operations",
    messageKey: "telemetry",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "telemetryEnabled" },
      { name: "endpoint", kind: "url", messageKey: "telemetryEndpoint" },
    ],
  },
  {
    section: "backup",
    group: "operations",
    messageKey: "backup",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "backupEnabled" },
      { name: "intervalHours", kind: "number", messageKey: "backupInterval" },
      { name: "retentionDays", kind: "number", messageKey: "backupRetention" },
      { name: "directory", kind: "text", messageKey: "backupDirectory" },
      { name: "encryptionKey", kind: "secret", messageKey: "backupEncryptionKey" },
    ],
  },
  {
    section: "security",
    group: "brand",
    messageKey: "security",
    fields: [
      { name: "sessionIdleMinutes", kind: "number", messageKey: "sessionIdle" },
      { name: "sessionAbsoluteHours", kind: "number", messageKey: "sessionAbsolute" },
      { name: "requireTotp", kind: "boolean", messageKey: "requireTotp" },
      { name: "allowedOrigins", kind: "text", messageKey: "allowedOrigins" },
    ],
  },
];
