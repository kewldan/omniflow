FROM oven/bun:1.3.14-alpine AS dependencies
ARG APP
WORKDIR /src
COPY package.json bun.lock bunfig.toml ./
COPY apps/${APP}/package.json apps/${APP}/package.json
COPY packages/api-client/package.json packages/api-client/package.json
COPY packages/ui/package.json packages/ui/package.json
RUN bun install --frozen-lockfile

FROM dependencies AS build
ARG APP
COPY . .
RUN bun --filter @omniflow/${APP} build

FROM node:26.5-alpine AS runtime
ARG APP
ENV APP=${APP}
ENV NODE_ENV=production
WORKDIR /app
COPY --from=build /src/apps/${APP}/.next/standalone ./
COPY --from=build /src/apps/${APP}/.next/static ./apps/${APP}/.next/static
EXPOSE 3000
CMD ["sh", "-c", "node apps/${APP}/server.js"]
