FROM oven/bun:1.3.14-alpine AS dependencies
WORKDIR /src
COPY package.json bun.lock bunfig.toml ./
COPY apps/web/package.json apps/web/package.json
COPY packages/api-client/package.json packages/api-client/package.json
COPY packages/ui/package.json packages/ui/package.json
RUN bun install --frozen-lockfile

FROM dependencies AS build
COPY . .
RUN bun --filter @omniflow/web build

FROM node:26.7-alpine AS runtime
ENV NODE_ENV=production
WORKDIR /app

# npm is removed because nothing here runs it.
#
# The image starts a built server with `node`; npm is never invoked. It arrives
# with the base image carrying its own bundled dependency tree — brace-expansion,
# ip-address, socks and the rest — and those are what an image scan reports,
# because they are real code present in the image whatever uses them. Deleting
# it removes the findings by removing the code rather than by silencing the
# scanner, and takes a few megabytes with it.
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx

COPY --from=build /src/apps/web/.next/standalone ./
COPY --from=build /src/apps/web/.next/static ./apps/web/.next/static
EXPOSE 3000
CMD ["node", "apps/web/server.js"]
