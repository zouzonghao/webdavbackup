FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

ARG TARGETARCH
COPY binaries/webdavbackup-linux-${TARGETARCH} /app/webdavbackup

RUN chmod +x /app/webdavbackup

ENTRYPOINT ["/app/webdavbackup"]
