import { defineConfig } from "orval";

export default defineConfig({
  omniflow: {
    input: "./api/openapi.yaml",
    output: {
      clean: true,
      client: "swr",
      httpClient: "fetch",
      mode: "single",
      target: "./packages/api-client/src/generated/omniflow.ts",
    },
  },
  omniflowSchemas: {
    input: "./api/openapi.yaml",
    output: {
      client: "zod",
      mode: "single",
      target: "./packages/api-client/src/generated/schemas.ts",
    },
  },
});
