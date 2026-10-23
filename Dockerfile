# syntax=docker/dockerfile:1

FROM node:22-alpine AS css
WORKDIR /src
COPY web/package.json web/tailwind.config.js ./
COPY web/static/css/input.css ./static/css/input.css
COPY web/templates ./templates
RUN npx --yes tailwindcss@3.4.17 -i ./static/css/input.css -o ./static/css/app.css --minify

FROM golang:1.27-alpine AS build
WORKDIR /src
RUN go install github.com/a-h/templ/cmd/templ@latest
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/static/css/app.css ./web/static/css/app.css
RUN /go/bin/templ generate
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tablekit ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tablekit-migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tablekit-bootstrap ./cmd/bootstrap

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S tablekit && adduser -S -G tablekit tablekit
WORKDIR /app
COPY --from=build /out/tablekit /app/tablekit
COPY --from=build /out/tablekit-migrate /app/tablekit-migrate
COPY --from=build /out/tablekit-bootstrap /app/tablekit-bootstrap
USER tablekit
EXPOSE 8080
ENTRYPOINT ["/app/tablekit"]
