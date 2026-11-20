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

export type FieldKind =
  | "text"
  | "url"
  | "number"
  | "boolean"
  | "textarea"
  | "secret"
  | "time"
  | "multiselect"
  | "select";

export type SectionField = {
  /** Key inside the section's document, or inside its secrets when `secret`. */
  name: string;
  kind: FieldKind;
  /** Suffix under `admin.installationSettings.fields` for the label and hint. */
  messageKey: string;
  /**
   * What the running installation does while the field is unsaved. A switch
   * renders it, so an unsaved "on" is not shown as "off" and then saved as
   * "off" by an operator who only meant to change something else.
   */
  defaultValue?: boolean;
  /** The closed list a `multiselect` picks from. */
  options?: readonly string[];
};

/** The operator notification streams that can be switched off. */
export const NOTIFICATION_KINDS = [
  "purchase",
  "renewal",
  "topup",
  "refund",
  "fulfillment_failure",
  "incident",
  "backup",
  "security",
  "support",
  "anomaly",
  "trial",
  "addon",
  "promo",
  "partner",
  "error",
  "infrastructure",
  "report",
] as const;

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
    // Every field here is read by a running process: the service name by
    // both web panels, the support contact and terms address by the bot, the
    // default language by the bot for a customer whose Telegram language it
    // does not speak, and the time zone by quiet hours and scheduled reports.
    // The public URL is not here: signed links and return addresses are built
    // from APP_PUBLIC_URL at start-up, and two answers to "where are we" is
    // one too many.
    fields: [
      { name: "serviceName", kind: "text", messageKey: "serviceName" },
      { name: "supportContact", kind: "url", messageKey: "supportContact" },
      { name: "termsUrl", kind: "url", messageKey: "termsUrl" },
      { name: "defaultLocale", kind: "text", messageKey: "defaultLocale" },
      { name: "timezone", kind: "text", messageKey: "timezone" },
    ],
  },
  // Credentials a process needs before it can start — the Remnawave token,
  // the bot token, the webhook secret, the backup key — stay in the
  // environment. The panel no longer offers to store a second copy that
  // nothing would read; what remains here is what the processes obey.
  {
    section: "remnawave",
    group: "integrations",
    messageKey: "remnawave",
    fields: [{ name: "reconcileIntervalMinutes", kind: "number", messageKey: "reconcileInterval" }],
  },
  {
    section: "operator_group",
    group: "integrations",
    messageKey: "operatorGroup",
    fields: [
      { name: "chatId", kind: "text", messageKey: "operatorChatId" },
      { name: "createTopics", kind: "boolean", messageKey: "createTopics", defaultValue: true },
      { name: "notificationCap", kind: "number", messageKey: "notificationCap" },
      { name: "windowMinutes", kind: "number", messageKey: "windowMinutes" },
      {
        name: "disabledKinds",
        kind: "multiselect",
        messageKey: "disabledKinds",
        options: NOTIFICATION_KINDS,
      },
    ],
  },
  {
    section: "bot",
    group: "integrations",
    messageKey: "bot",
    fields: [
      { name: "languages", kind: "multiselect", messageKey: "botLanguages", options: ["ru", "en"] },
      { name: "miniAppUrl", kind: "url", messageKey: "miniAppUrl" },
      { name: "cabinetOnly", kind: "boolean", messageKey: "cabinetOnly" },
      {
        name: "hiddenMenu",
        kind: "multiselect",
        messageKey: "hiddenMenu",
        options: [
          "plans",
          "subscription",
          "connect",
          "devices",
          "orders",
          "wallet",
          "gifts",
          "support",
          "news",
          "referral",
          "settings",
        ],
      },
    ],
  },
  {
    section: "support",
    group: "integrations",
    messageKey: "support",
    fields: [
      {
        name: "mode",
        kind: "select",
        messageKey: "supportMode",
        options: ["both", "tickets", "contact"],
      },
      { name: "introRu", kind: "textarea", messageKey: "supportIntroRu" },
      { name: "introEn", kind: "textarea", messageKey: "supportIntroEn" },
      {
        name: "notifyNewTicket",
        kind: "boolean",
        messageKey: "notifyNewTicket",
        defaultValue: true,
      },
      {
        name: "notifyCustomerReply",
        kind: "boolean",
        messageKey: "notifyCustomerReply",
        defaultValue: true,
      },
      {
        name: "notifySlaBreach",
        kind: "boolean",
        messageKey: "notifySlaBreach",
        defaultValue: true,
      },
    ],
  },
  {
    section: "access",
    group: "integrations",
    messageKey: "access",
    fields: [
      {
        name: "mode",
        kind: "select",
        messageKey: "accessMode",
        options: ["open", "invite", "closed"],
      },
      { name: "allowReferrals", kind: "boolean", messageKey: "allowReferrals", defaultValue: true },
      { name: "allowedCampaigns", kind: "text", messageKey: "allowedCampaigns" },
      { name: "inviteCodes", kind: "text", messageKey: "inviteCodes" },
    ],
  },
  {
    section: "moderation",
    group: "integrations",
    messageKey: "moderation",
    fields: [
      { name: "filterDisplayNames", kind: "boolean", messageKey: "filterDisplayNames" },
      { name: "blockedNamePatterns", kind: "text", messageKey: "blockedNamePatterns" },
    ],
  },
  {
    section: "reports",
    group: "operations",
    messageKey: "reports",
    fields: [
      {
        name: "periods",
        kind: "multiselect",
        messageKey: "reportPeriods",
        options: ["day", "week", "month"],
      },
      { name: "time", kind: "time", messageKey: "reportTime" },
    ],
  },
  {
    section: "maintenance",
    group: "operations",
    messageKey: "maintenance",
    fields: [
      { name: "autoEnable", kind: "boolean", messageKey: "autoEnable", defaultValue: true },
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
      { name: "fulfillmentAlertAttempts", kind: "number", messageKey: "fulfillmentAlertAttempts" },
      { name: "quietHoursStart", kind: "time", messageKey: "quietHoursStart" },
      { name: "quietHoursEnd", kind: "time", messageKey: "quietHoursEnd" },
    ],
  },
  {
    section: "telemetry",
    group: "operations",
    messageKey: "telemetry",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "telemetryEnabled", defaultValue: true },
    ],
  },
  {
    section: "backup",
    group: "operations",
    messageKey: "backup",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "backupEnabled" },
      { name: "intervalHours", kind: "number", messageKey: "backupInterval" },
      { name: "timeOfDay", kind: "time", messageKey: "backupTimeOfDay" },
      { name: "retentionDays", kind: "number", messageKey: "backupRetention" },
      { name: "telegramChatId", kind: "text", messageKey: "backupTelegramChat" },
      { name: "telegramThreadId", kind: "number", messageKey: "backupTelegramThread" },
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
    ],
  },
];
