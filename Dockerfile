FROM --platform=$BUILDPLATFORM golang:1.27 AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
	-trimpath \
	-ldflags "-s -w -X main.version=$VERSION" \
	-o /out/mc-agents-operator ./cmd/mc-agents-operator

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/mc-agents-operator /usr/local/bin/mc-agents-operator

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mc-agents-operator"]
