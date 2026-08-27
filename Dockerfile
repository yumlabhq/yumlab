# goreleaser drops the prebuilt static binary next to this file.
FROM scratch

COPY yumlab /yumlab

# Needed to reach api.github.com; nothing else is required at runtime.
COPY --from=alpine:3.20 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/yumlab"]
CMD ["scan"]
