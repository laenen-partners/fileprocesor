# Build stage
FROM golang:1.25 AS build

WORKDIR /src

# Copy dependency files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build static binary
RUN CGO_ENABLED=0 go build -o /fproc ./cmd/fproc

# Run stage
FROM gcr.io/distroless/static-debian12

COPY --from=build /fproc /fproc

EXPOSE 3001

ENTRYPOINT ["/fproc"]
