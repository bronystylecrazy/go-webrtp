# go-webtransport-rtp
Golang library for streaming RTP packet from RTSP source directly to web in real-time.

## Usage

```go
package main

func main() {
	
}
```

## Development

1. Generate self-signed certificate for TLS connection

```bash
mkdir -p .local
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout .local/x509-key.pem -out .local/x509-cer.pem -days 365 -nodes -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```
