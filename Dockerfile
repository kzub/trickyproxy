FROM golang:1.18-alpine AS build

WORKDIR /usr/src/trickyproxy

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o trickyproxy

# Deploy
FROM gcr.io/distroless/base-debian10

ARG REVISION
ARG GIT_BRANCH

LABEL git-rev="${REVISION}" \
      git-branch="${GIT_BRANCH}"

WORKDIR /usr/bin

COPY --from=build /usr/src/trickyproxy/trickyproxy ./

ENTRYPOINT ["trickyproxy"]