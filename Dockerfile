FROM golang:1.25.5-alpine AS builder
RUN apk add --no-cache git ca-certificates build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /usr/local/bin/cronjob-controller ./main.go

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=builder /usr/local/bin/cronjob-controller /usr/local/bin/cronjob-controller
USER 1000
ENTRYPOINT ["/usr/local/bin/cronjob-controller"]
