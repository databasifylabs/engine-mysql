# check=skip=InvalidDefaultArgInFrom
ARG GO_IMAGE
ARG BASE_IMAGE

FROM ghcr.io/databasifylabs/engine-sdk:1.0.0@sha256:cf066886a156193196e9d043e3f75e9084928948e40bdd950b2306f2b340d901 AS sdk

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS rclone
ARG TARGETOS
ARG TARGETARCH
ARG RCLONE_VERSION
WORKDIR /build
RUN set -eux; \
    go mod init rclone-build; \
    go get "github.com/rclone/rclone@v${RCLONE_VERSION}"; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-s -w -X github.com/rclone/rclone/fs.Version=v${RCLONE_VERSION}" \
        -o /out/rclone github.com/rclone/rclone

FROM ${BASE_IMAGE} AS binlog
ARG RELEASE
WORKDIR /build
RUN set -eux; \
    arch=$(uname -m); \
    line=${RELEASE%.*}; \
    rpm --import /etc/pki/rpm-gpg/RPM-GPG-KEY-mysql; \
    curl -fsSLO "https://repo.mysql.com/yum/mysql-${line}-community/el/9/${arch}/mysql-community-client-${RELEASE}-1.el9.${arch}.rpm"; \
    rpm -K "mysql-community-client-${RELEASE}-1.el9.${arch}.rpm" | grep -q 'digests signatures OK'; \
    microdnf install -y --setopt=install_weak_deps=0 cpio; \
    rpm2cpio "mysql-community-client-${RELEASE}-1.el9.${arch}.rpm" | cpio -idm ./usr/bin/mysqlbinlog; \
    ./usr/bin/mysqlbinlog --version | grep -F "Ver ${RELEASE} "

FROM ${BASE_IMAGE}
ARG RELEASE
ARG SDK_VERSION
ARG LINE
ARG PACKAGES
RUN --mount=type=bind,source=images/mysql/repositories.sh,target=/tmp/repositories.sh \
    set -eux; \
    bash /tmp/repositories.sh; \
    microdnf install -y --setopt=install_weak_deps=0 $(printf '%s\n' $PACKAGES | sed 's/=/-/'); \
    microdnf clean all; \
    rm -f /usr/local/bin/gosu
COPY --from=rclone /out/rclone /usr/local/bin/rclone
COPY --from=binlog /build/usr/bin/mysqlbinlog /usr/bin/mysqlbinlog
RUN set -eux; \
    groupmod -g 64999 mysql; \
    usermod -u 64999 -g 64999 mysql; \
    find / -xdev \( -uid 999 -o -gid 999 \) -exec chown -h 64999:64999 {} +
COPY --from=sdk /databasify /usr/local/bin/databasify
LABEL org.opencontainers.image.source="https://github.com/databasifylabs/engine-mysql" \
      io.databasify.engine="mysql" \
      io.databasify.release="${RELEASE}" \
      io.databasify.line="${LINE}" \
      io.databasify.sdk="${SDK_VERSION}"
ENV DATABASIFY_ENGINE=mysql \
    DATABASIFY_RELEASE=${RELEASE} \
    DATABASIFY_LINE=${LINE}
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/databasify", "serve"]
CMD []
