FROM node:24.22.3-alpine@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5 AS web
WORKDIR /app
COPY package.json package-lock.json tsconfig.web.json ./
COPY src/web ./src/web
RUN npm ci --ignore-scripts && npm run build

FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS backend
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /butaco ./cmd/butaco

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates && addgroup -S butaco && adduser -S -G butaco butaco
COPY --from=backend /butaco /butaco
COPY --from=web /app/dist/public /public
ENV STATIC_DIR=/public
USER butaco
EXPOSE 8080
ENTRYPOINT ["/butaco"]
