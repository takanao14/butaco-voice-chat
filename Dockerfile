FROM node:22-alpine AS web
WORKDIR /app
COPY package.json package-lock.json tsconfig.web.json ./
COPY src/web ./src/web
RUN npm ci --ignore-scripts && npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /butako ./cmd/butako

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S butako && adduser -S -G butako butako
COPY --from=backend /butako /butako
COPY --from=web /app/dist/public /public
ENV STATIC_DIR=/public
USER butako
EXPOSE 8080
ENTRYPOINT ["/butako"]
