FROM busybox:1.37

COPY bin/migrate-catalogs-v0-to-v1 /usr/local/bin/migrate-catalogs-v0-to-v1
COPY bin/migrate-operators-v0-to-v1 /usr/local/bin/migrate-operators-v0-to-v1

USER 65532:65532
