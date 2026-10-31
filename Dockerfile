FROM node:24.22.2-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85 AS web
WORKDIR /app
COPY package.json package-lock.json tsconfig.web.json ./
COPY src/web ./src/web
RUN npm ci --ignore-scripts && npm run build

FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS backend
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /butaco ./cmd/butaco

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates && addgroup -S butaco && adduser -S -G butaco butaco
COPY --from=backend /butaco /butaco
COPY --from=web /app/dist/public /public
ENV STATIC_DIR=/public
USER butaco
EXPOSE 8080
ENTRYPOINT ["/butaco"]
