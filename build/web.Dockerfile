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
COPY --from=build /src/apps/web/.next/standalone ./
COPY --from=build /src/apps/web/.next/static ./apps/web/.next/static
EXPOSE 3000
CMD ["node", "apps/web/server.js"]
