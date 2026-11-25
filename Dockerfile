# zharf serve in a container: the Ollama and OpenAI APIs on port 11434, using the key in
# ZHARF_API_KEY.
#
#   docker build -t zharf .
#   docker run -d --name zharf -p 127.0.0.1:11434:11434 -e ZHARF_API_KEY=zk-... zharf
#
# The server listens on 0.0.0.0 inside the container. Publish the port on 127.0.0.1 (as above)
# unless you mean other computers to spend your credit.

FROM golang:1.25 AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/ZharfAI/zharf/version.Version=${VERSION}" \
      -o /out/zharf .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zharf /usr/local/bin/zharf
ENV HOME=/home/nonroot \
    ZHARF_KEYRING=file \
    ZHARF_HOST=0.0.0.0 \
    ZHARF_PORT=11434
EXPOSE 11434
ENTRYPOINT ["/usr/local/bin/zharf"]
CMD ["serve", "--host", "0.0.0.0"]
