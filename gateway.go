package logcache

import (
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"code.cloudfoundry.org/go-log-cache/rpc/logcache_v1"
	"code.cloudfoundry.org/log-cache/internal/gateway"
)

// Gateway provides a RESTful API into LogCache's gRPC API.
type Gateway struct {
	log *log.Logger

	logCacheAddr    string
	groupReaderAddr string

	gatewayAddr         string
	lis                 net.Listener
	blockOnStart        bool
	logCacheDialOpts    []grpc.DialOption
	groupReaderDialOpts []grpc.DialOption
}

// NewGateway creates a new Gateway. It will listen on the gatewayAddr and
// submit requests via gRPC to the LogCache on logCacheAddr. Start() must be
// invoked before using the Gateway.
func NewGateway(logCacheAddr, groupReaderAddr, gatewayAddr string, opts ...GatewayOption) *Gateway {
	g := &Gateway{
		log: log.New(ioutil.Discard, "", 0),

		logCacheAddr:    logCacheAddr,
		groupReaderAddr: groupReaderAddr,

		gatewayAddr: gatewayAddr,
	}

	for _, o := range opts {
		o(g)
	}

	return g
}

// GatewayOption configures a Gateway.
type GatewayOption func(*Gateway)

// WithGatewayLogger returns a GatewayOption that configures the logger for
// the Gateway. It defaults to no logging.
func WithGatewayLogger(l *log.Logger) GatewayOption {
	return func(g *Gateway) {
		g.log = l
	}
}

// WithGatewayBlock returns a GatewayOption that determines if Start launches
// a go-routine or not. It defaults to launching a go-routine. If this is set,
// start will block on serving the HTTP endpoint.
func WithGatewayBlock() GatewayOption {
	return func(g *Gateway) {
		g.blockOnStart = true
	}
}

// WithGatewayLogCacheDialOpts returns a GatewayOption that sets grpc.DialOptions on the
// log-cache dial
func WithGatewayLogCacheDialOpts(opts ...grpc.DialOption) GatewayOption {
	return func(g *Gateway) {
		g.logCacheDialOpts = opts
	}
}

// WithGatewayGroupReaderDialOpts returns a GatewayOption that sets grpc.DialOptions on the
// log-cache group reader dial
func WithGatewayGroupReaderDialOpts(opts ...grpc.DialOption) GatewayOption {
	return func(g *Gateway) {
		g.groupReaderDialOpts = opts
	}
}

// Start starts the gateway to start receiving and forwarding requests. It
// does not block unless WithGatewayBlock was set.
func (g *Gateway) Start() {
	lis, err := net.Listen("tcp", g.gatewayAddr)
	if err != nil {
		g.log.Fatalf("failed to listen on addr %s: %s", g.gatewayAddr, err)
	}
	g.lis = lis
	g.log.Printf("listening on %s...", lis.Addr().String())

	if g.blockOnStart {
		g.listenAndServe()
		return
	}

	go g.listenAndServe()
}

// Addr returns the address the gateway is listening on. Start must be called
// first.
func (g *Gateway) Addr() string {
	return g.lis.Addr().String()
}

func (g *Gateway) listenAndServe() {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(
			runtime.MIMEWildcard, gateway.NewPromqlMarshaler(&runtime.JSONPb{OrigName: true, EmitDefaults: true}),
		),
	)

	runtime.HTTPError = g.httpErrorHandler

	conn, err := grpc.Dial(g.logCacheAddr, g.logCacheDialOpts...)
	if err != nil {
		g.log.Fatalf("failed to dial Log Cache: %s", err)
	}

	err = logcache_v1.RegisterEgressHandlerClient(
		context.Background(),
		mux,
		logcache_v1.NewEgressClient(conn),
	)
	if err != nil {
		g.log.Fatalf("failed to register LogCache handler: %s", err)
	}

	err = logcache_v1.RegisterPromQLQuerierHandlerClient(
		context.Background(),
		mux,
		logcache_v1.NewPromQLQuerierClient(conn),
	)
	if err != nil {
		g.log.Fatalf("failed to register PromQLQuerier handler: %s", err)
	}

	gconn, err := grpc.Dial(g.groupReaderAddr, g.groupReaderDialOpts...)
	if err != nil {
		g.log.Fatalf("failed to dial Shard Group Reader: %s", err)
	}

	err = logcache_v1.RegisterShardGroupReaderHandlerClient(
		context.Background(),
		mux,
		logcache_v1.NewShardGroupReaderClient(gconn),
	)
	if err != nil {
		g.log.Fatalf("failed to register ShardGroupReader handler: %s", err)
	}

	server := &http.Server{Handler: mux}
	if err := server.Serve(g.lis); err != nil {
		g.log.Fatalf("failed to serve HTTP endpoint: %s", err)
	}
}

type errorBody struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

func (g *Gateway) httpErrorHandler(
	ctx context.Context,
	mux *runtime.ServeMux,
	marshaler runtime.Marshaler,
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	if r.URL.Path != "/api/v1/query" && r.URL.Path != "/api/v1/query_range" {
		runtime.DefaultHTTPError(ctx, mux, marshaler, w, r, err)
		return
	}

	const fallback = `{"error": "failed to marshal error message"}`

	w.Header().Del("Trailer")
	w.Header().Set("Content-Type", marshaler.ContentType())

	body := &errorBody{
		Status:    "error",
		ErrorType: "internal",
		Error:     grpc.ErrorDesc(err),
	}

	buf, merr := marshaler.Marshal(body)
	if merr != nil {
		g.log.Printf("Failed to marshal error message %q: %v", body, merr)
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := io.WriteString(w, fallback); err != nil {
			g.log.Printf("Failed to write response: %v", err)
		}
		return
	}

	w.WriteHeader(runtime.HTTPStatusFromCode(grpc.Code(err)))
	if _, err := w.Write(buf); err != nil {
		g.log.Printf("Failed to write response: %v", err)
	}
}
