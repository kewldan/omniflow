import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";

const withNextIntl = createNextIntlPlugin();

const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  reactCompiler: true,
  reactStrictMode: true,
  transpilePackages: ["@omniflow/api-client", "@omniflow/ui"],
  // `/v1` is routed to the API by the reverse proxy in production and by the
  // middleware when nothing is in front of the stack. It is deliberately not a
  // `rewrites()` entry: those are evaluated at build time and baked into the
  // routes manifest, so a published image could never be pointed at a different
  // API at deploy time.
};

export default withNextIntl(nextConfig);
