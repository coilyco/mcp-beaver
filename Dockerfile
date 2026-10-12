# mcp-beaver - the ONE generic runtime image. It carries no guardfile: the spec is
# mounted or COPYed in by the consumer (deploy#40 / mcp-beaver#8's chart) and named
# on the command line. The same binary drives every `.mcp.kdl`; only the spec
# varies. The image serves MCP over HTTP/SSE and never binds stdio.
#
#   docker build -t mcp-beaver .
#   docker run -p 8080:8080 -e SKILLSMP_API_KEY \
#     -v $PWD/examples/skillsmp.mcp.kdl:/spec/skillsmp.mcp.kdl \
#     mcp-beaver serve /spec/skillsmp.mcp.kdl --http :8080
#
# umbra is a private forgejo module, fetched anonymously; GOPRIVATE keeps it
# off the public proxy/sumdb. The build is a CI consequence of a landed commit,
# per docs/FEATURES.md - it holds no registry creds.
FROM golang:1.25 AS build
ENV GOPRIVATE=forgejo.coilysiren.me
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-beaver ./cmd/mcp-beaver

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp-beaver /usr/local/bin/mcp-beaver
# Baked-in worked example; a real deployment mounts its own /spec/<name>.mcp.kdl.
COPY examples /spec/examples
EXPOSE 8080
ENTRYPOINT ["mcp-beaver"]
CMD ["serve", "/spec/examples/forgejo-issues.mcp.kdl", "--http", ":8080"]
