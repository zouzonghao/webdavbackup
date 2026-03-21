FROM alpine:3.19 AS builder

RUN apk add --no-cache go git musl-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-s -w -linkmode external -extldflags "-static"' \
    -o /output/webdav-backup .

FROM scratch AS binary

COPY --from=builder /output/webdav-backup /webdav-backup
