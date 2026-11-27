# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f apps/web/Dockerfile .
FROM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS build
RUN corepack enable
WORKDIR /repo
COPY pnpm-lock.yaml pnpm-workspace.yaml package.json .npmrc ./
COPY apps/web/package.json apps/web/
COPY contracts/node/package.json contracts/node/
RUN pnpm install --frozen-lockfile --filter @hedvi/web...
COPY apps/web apps/web
# Public documentation and contact values are embedded at build time.
# HEDVI_API_INTERNAL_URL is read at runtime by the server-side proxy.
ARG HEDVI_APP_HOST
ARG HEDVI_PUBLIC_API_URL=https://api.hedvi.dev
ARG NEXT_PUBLIC_HEDVI_SUPPORT_EMAIL=support@example.invalid
ARG NEXT_PUBLIC_HEDVI_SOURCE_URL=https://github.com/albinjarnmark/hedvi
RUN pnpm --filter @hedvi/web build

FROM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5
ENV NODE_ENV=production PORT=3030 HOSTNAME=0.0.0.0
WORKDIR /app
COPY --from=build /repo/apps/web/.next/standalone ./
COPY --from=build /repo/apps/web/.next/static ./apps/web/.next/static
COPY --from=build /repo/apps/web/public ./apps/web/public
USER node
EXPOSE 3030
CMD ["node", "apps/web/server.js"]
