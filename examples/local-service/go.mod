module github.com/zap-proto/zip/examples/local-service

go 1.26.8

require (
	github.com/hanzoai/sqlite v0.5.10
	github.com/zap-proto/zip v1.36.47
)

require (
	github.com/andybalholm/brotli v1.2.1 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gofiber/schema v1.7.1 // indirect
	github.com/gofiber/utils/v2 v2.0.4 // indirect
	github.com/google/uuid v1.6.1-0.20241114170450-2d3c2a9cc518 // indirect
	github.com/gorilla/rpc v1.2.1 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/hanzoai/csqlite v0.1.2 // indirect
	github.com/hanzoai/sqlcipher v0.1.1 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/luxfi/accel v1.2.4 // indirect
	github.com/luxfi/crypto v1.20.2 // indirect
	github.com/luxfi/log v1.4.3 // indirect
	github.com/luxfi/mdns v0.1.1 // indirect
	github.com/luxfi/metric v1.10.0 // indirect
	github.com/luxfi/zap v1.2.7 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.70.0 // indirect
	github.com/zap-proto/fiber/v3 v3.2.1 // indirect
	github.com/zap-proto/go v1.8.3 // indirect
	github.com/zap-proto/http v0.3.5 // indirect
	github.com/zap-proto/mcp v1.0.5 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.51.0 // indirect
)

// The example runs against this checkout, so a change to zip and the example
// that uses it are tested together. A copy outside the repository drops this line.
replace github.com/zap-proto/zip => ../..

tool github.com/zap-proto/zip/cmd/zipdoc
