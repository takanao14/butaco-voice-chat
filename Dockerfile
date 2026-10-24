FROM node:24.22.1-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /app
COPY package.json package-lock.json tsconfig.web.json ./
COPY src/web ./src/web
RUN npm ci --ignore-scripts && npm run build

FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS backend
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /butaco ./cmd/butaco

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates && addgroup -S butaco && adduser -S -G butaco butaco
COPY --from=backend /butaco /butaco
COPY --from=web /app/dist/public /public
ENV STATIC_DIR=/public
USER butaco
EXPOSE 8080
ENTRYPOINT ["/butaco"]
