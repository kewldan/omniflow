import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";

const withNextIntl = createNextIntlPlugin();

const nextConfig: NextConfig = {
  output: "standalone",
  transpilePackages: ["@omniflow/api-client", "@omniflow/ui"],
};

export default withNextIntl(nextConfig);
