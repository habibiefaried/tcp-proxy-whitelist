FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o wlistproxy .

FROM alpine:latest

RUN apk --no-cache add ca-certificates
COPY --from=builder /app/wlistproxy /usr/local/bin/wlistproxy

EXPOSE 8080
ENTRYPOINT ["wlistproxy"]
