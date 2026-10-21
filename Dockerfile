# Runtime image for the refactored Inference control plane.
# Build the static binary with CGO_ENABLED=0 before invoking docker build.
FROM scratch

USER 65534:65534
COPY .build/ani-inference-service /ani-inference-service
COPY configs/config.yaml /configs/config.yaml

ENTRYPOINT ["/ani-inference-service"]
