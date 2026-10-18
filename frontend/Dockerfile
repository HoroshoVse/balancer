# The build output is static, so build once on the native platform
FROM --platform=$BUILDPLATFORM node:22-alpine AS builder

WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.27-alpine
# API_UPSTREAM: where nginx proxies /api (the backend's API_ADDR)
# NGINX_PORT: UI port; not 80, which load balancers commonly use on the same host
ENV API_UPSTREAM=127.0.0.1:8080 \
    NGINX_PORT=3000
COPY nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 3000
