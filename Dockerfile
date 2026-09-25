FROM node:22.23.3-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web
WORKDIR /app
COPY package.json package-lock.json tsconfig.web.json ./
COPY src/web ./src/web
RUN npm ci --ignore-scripts && npm run build

FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS backend
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /butako ./cmd/butako

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates && addgroup -S butako && adduser -S -G butako butako
COPY --from=backend /butako /butako
COPY --from=web /app/dist/public /public
ENV STATIC_DIR=/public
USER butako
EXPOSE 8080
ENTRYPOINT ["/butako"]
