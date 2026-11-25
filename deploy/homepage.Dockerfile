FROM node:26.13.1-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM ghcr.io/nginx/nginx-unprivileged:1.30.8-alpine-slim@sha256:04c98c78c1de60ddeada00d7d2207dcd146ecd07b61b9941c9a5799cfb258d28
COPY --from=web /web/dist/ /usr/share/nginx/html/
COPY web/demo/workspace.json /usr/share/bugsnitch/workspace.json
COPY deploy/homepage.nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
