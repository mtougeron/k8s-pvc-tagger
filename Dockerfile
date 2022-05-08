FROM golang:1.18-alpine AS builder

ARG VERSION=0.0.1
ARG TARGETARCH

ENV APP_NAME=k8s-aws-ebs-tagger \
    GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=$TARGETARCH

# Move to working directory /build
WORKDIR /build

# Copy and download dependency using go mod
COPY go.mod go.sum ./
RUN go mod download

# Copy the code into the container
COPY . .

ENV APP_VERSION=$VERSION

# Build the application
RUN date +%s > buildtime
RUN APP_BUILD_TIME=$(cat buildtime); \
    go build -ldflags="-X 'main.buildTime=${APP_BUILD_TIME}' -X 'main.buildVersion=${APP_VERSION}'" -o ${APP_NAME} .

# Move to /dist directory as the place for resulting binary folder
WORKDIR /app 

# Copy binary from build to main folder
RUN cp /build/${APP_NAME} .

RUN addgroup -S k8s-aws-ebs-tagger && adduser -S k8s-aws-ebs-tagger -G k8s-aws-ebs-tagger

# Build a small image
FROM scratch
COPY --from=builder /etc/passwd /etc/passwd
USER k8s-aws-ebs-tagger
# https://github.com/aws/aws-sdk-go/issues/2322
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/${APP_NAME} /

ENTRYPOINT ["/k8s-aws-ebs-tagger"]
