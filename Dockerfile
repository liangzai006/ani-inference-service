# Runtime image for the refactored Inference control plane.
# Build the static binary with CGO_ENABLED=0 before invoking docker build.
FROM scratch

USER 65534:65534
COPY --chmod=0755 .build/ani-inference-service /ani-inference-service
COPY --chmod=0755 configs /configs
COPY --chmod=0644 configs/config.yaml /configs/config.yaml

ENTRYPOINT ["/ani-inference-service"]
