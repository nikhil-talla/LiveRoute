FROM --platform=linux/amd64 golang@sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c AS plugins

RUN GOBIN=/out go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 \
    && GOBIN=/out go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

FROM --platform=linux/amd64 bmigeri/devcon-cpp@sha256:369f7744d6f9632b1c8142981f01c7b4c98db51b0096686dbd25f9ebb9eaa6f4

COPY --from=plugins /out/protoc-gen-go /usr/local/bin/protoc-gen-go
COPY --from=plugins /out/protoc-gen-go-grpc /usr/local/bin/protoc-gen-go-grpc

RUN test "$(protoc --version)" = "libprotoc 31.1" \
    && test "$(protoc-gen-go --version)" = "protoc-gen-go v1.36.6" \
    && test "$(protoc-gen-go-grpc --version)" = "protoc-gen-go-grpc 1.5.1"
