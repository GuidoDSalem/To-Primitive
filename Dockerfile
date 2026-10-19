FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/to-primitive .

FROM alpine:3.20
RUN apk add --no-cache ffmpeg && mkdir /reels && chown nobody /reels
COPY --from=build /out/to-primitive /usr/local/bin/to-primitive
# Finished reels are kept here; mount a volume to keep them across rebuilds.
ENV REELS_DIR=/reels
VOLUME /reels
USER nobody
EXPOSE 8080
ENTRYPOINT ["to-primitive"]
