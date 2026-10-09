FROM alpine:3.23

ARG TARGETPLATFORM

RUN apk add --no-cache ca-certificates tzdata

COPY bin/ /tmp/bin/

RUN case "${TARGETPLATFORM}" in \
      "linux/386")      BIN="OwlShack-linux-386/OwlShack-linux-386" ;; \
      "linux/amd64")    BIN="OwlShack-linux-amd64/OwlShack-linux-amd64" ;; \
      "linux/arm/v6")   BIN="OwlShack-linux-armv6/OwlShack-linux-armv6" ;; \
      "linux/arm/v7")   BIN="OwlShack-linux-armv7/OwlShack-linux-armv7" ;; \
      "linux/arm64"*)   BIN="OwlShack-linux-arm64/OwlShack-linux-arm64" ;; \
      "linux/ppc64le")  BIN="OwlShack-linux-ppc64le/OwlShack-linux-ppc64le" ;; \
      "linux/riscv64")  BIN="OwlShack-linux-riscv64/OwlShack-linux-riscv64" ;; \
      "linux/s390x")    BIN="OwlShack-linux-s390x/OwlShack-linux-s390x" ;; \
      *) echo "unsupported platform: ${TARGETPLATFORM}" && exit 1 ;; \
    esac && \
    cp "/tmp/bin/${BIN}" /usr/local/bin/OwlShack && \
    chmod +x /usr/local/bin/OwlShack && \
    rm -rf /tmp/bin

VOLUME [ "/data" ]

# The web UI, and the MeshCore app's ports for companions that let it in.
EXPOSE 8080 5000-5015

WORKDIR /data

CMD ["/usr/local/bin/OwlShack"]
